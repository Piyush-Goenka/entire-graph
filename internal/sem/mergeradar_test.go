package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

const (
	radarBasePricing = "package shop\n\n// Price returns the price in paise.\nfunc Price(item string) int {\n\treturn 100\n}\n"
	radarBaseInvoice = "package shop\n\nfunc Header() string {\n\treturn \"INVOICE\"\n}\n"
	radarRupees      = "package shop\n\n// Price returns the price in rupees.\nfunc Price(item string) float64 {\n\treturn 1.0\n}\n"
	radarInvoicing   = radarBaseInvoice + "\n// Invoice totals prices in paise.\nfunc Invoice(items []string) int {\n\ttotal := 0\n\tfor _, item := range items {\n\t\ttotal += Price(item)\n\t}\n\treturn total\n}\n"
)

// radarRepo builds a repo with a base commit on main and returns its path.
func radarRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "go.mod", "module example.com/shop\n\ngo 1.22\n")
	for path, content := range files {
		writeFile(t, repo, path, content)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "base")
	git(t, repo, "branch", "-M", "main")
	return repo
}

// radarBranch creates branch from main, applies files, and commits with the
// given message parts (each becomes a paragraph, so trailers work).
func radarBranch(t *testing.T, repo, branch string, files map[string]string, message ...string) {
	t.Helper()
	git(t, repo, "checkout", "-q", "main")
	git(t, repo, "checkout", "-q", "-b", branch)
	for path, content := range files {
		writeFile(t, repo, path, content)
	}
	git(t, repo, "add", "-A")
	args := []string{"commit", "-q", "-a"}
	for _, part := range message {
		args = append(args, "-m", part)
	}
	git(t, repo, args...)
	git(t, repo, "checkout", "-q", "main")
}

func TestAnalyzeMergeFindsDependencyConflict(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")
	radarBranch(t, repo, "invoicing", map[string]string{"invoice.go": radarInvoicing}, "invoicing on paise")

	report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "invoicing", 2)
	if err != nil {
		t.Fatal(err)
	}
	if report.A.Ref != "rupees" || report.B.Ref != "invoicing" {
		t.Fatalf("sides = %q, %q", report.A.Ref, report.B.Ref)
	}
	if len(report.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %+v", len(report.Conflicts), report.Conflicts)
	}
	c := report.Conflicts[0]
	if c.Kind != ConflictDependency || c.Confidence != ConfidencePotential {
		t.Fatalf("kind/confidence = %s/%s", c.Kind, c.Confidence)
	}
	if c.ChangedOn != "rupees" || c.Changed.Name != "Price" || c.Changed.Path != "pricing.go" {
		t.Fatalf("changed side wrong: %+v", c)
	}
	if c.Consumer == nil || c.ConsumerOn != "invoicing" || c.Consumer.Name != "Invoice" || c.Consumer.Path != "invoice.go" || c.Consumer.Type != "added" {
		t.Fatalf("consumer side wrong: %+v", c.Consumer)
	}
	if len(c.Via) != 0 {
		t.Fatalf("direct reference should have no via, got %+v", c.Via)
	}
	if c.Evidence != EvidenceStructural || len(c.EvidenceNotes) != 0 {
		t.Fatalf("a fully resolved reference must stay structural with no notes, got %q %v", c.Evidence, c.EvidenceNotes)
	}
	if !strings.Contains(c.Explanation, "never executed") {
		t.Fatalf("explanation = %q", c.Explanation)
	}
}

func TestAnalyzeMergeIgnoresCompatibleBranches(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")
	footer := radarBaseInvoice + "\nfunc Footer() string {\n\treturn \"thanks\"\n}\n"
	radarBranch(t, repo, "footer", map[string]string{"invoice.go": footer}, "add footer")

	report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "footer", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", report.Conflicts)
	}
	if report.A.Entities == 0 || report.B.Entities == 0 {
		t.Fatalf("both sides should report changes: %+v %+v", report.A, report.B)
	}
}

func TestAnalyzeMergeFindsDirectConflict(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")
	discounted := "package shop\n\n// Price applies a discount, still in paise.\nfunc Price(item string) int {\n\treturn 90\n}\n"
	radarBranch(t, repo, "discount", map[string]string{"pricing.go": discounted}, "discount")

	report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "discount", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", report.Conflicts)
	}
	c := report.Conflicts[0]
	if c.Kind != ConflictDirect || c.Changed.Name != "Price" || c.Consumer == nil || c.Consumer.Name != "Price" {
		t.Fatalf("direct conflict wrong: %+v", c)
	}
}

func TestAnalyzeMergeFollowsIndirectReferenceAtDepthTwo(t *testing.T) {
	subtotal := radarBasePricing + "\nfunc Subtotal(items []string) int {\n\ttotal := 0\n\tfor _, item := range items {\n\t\ttotal += Price(item)\n\t}\n\treturn total\n}\n"
	repo := radarRepo(t, map[string]string{"pricing.go": subtotal, "invoice.go": radarBaseInvoice})
	rupees := strings.Replace(subtotal, "func Price(item string) int {\n\treturn 100\n}", "func Price(item string) float64 {\n\treturn 1.0\n}", 1)
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": rupees}, "pricing in rupees")
	total := radarBaseInvoice + "\nfunc Total(items []string) int {\n\treturn Subtotal(items) + 18\n}\n"
	radarBranch(t, repo, "total", map[string]string{"invoice.go": total}, "add total")

	shallow, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "total", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range shallow.Conflicts {
		if c.Consumer != nil && c.Consumer.Name == "Total" {
			t.Fatalf("depth 1 must not reach Total through Subtotal: %+v", c)
		}
	}

	deep, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "total", 2)
	if err != nil {
		t.Fatal(err)
	}
	var found *MergeConflict
	for i := range deep.Conflicts {
		if deep.Conflicts[i].Consumer != nil && deep.Conflicts[i].Consumer.Name == "Total" {
			found = &deep.Conflicts[i]
		}
	}
	if found == nil {
		t.Fatalf("depth 2 should reach Total via Subtotal, got %+v", deep.Conflicts)
	}
	if found.Changed.Name != "Price" || len(found.Via) != 1 || shortEntityName(found.Via[0].Name) != "Subtotal" {
		t.Fatalf("via chain wrong: %+v", found)
	}
	if !strings.Contains(found.Explanation, "through Subtotal") {
		t.Fatalf("explanation should name the intermediate: %q", found.Explanation)
	}
}

// writeFakeCheckpoint stores a checkpoint tree shaped like the Entire CLI's
// refs backend (root metadata.json plus one session directory) and points
// refs/entire/checkpoints/<shard>/<id> at it. summary may be empty to
// exercise the prompt fallback.
// fakeCheckpointTree writes a checkpoint tree shaped like Entire's: a root
// metadata.json plus one session directory holding metadata.json and
// prompt.txt. It returns the tree id.
func fakeCheckpointTree(t *testing.T, repo, id, summary, prompt string, filesTouched []string) string {
	t.Helper()
	quoted := make([]string, 0, len(filesTouched))
	for _, f := range filesTouched {
		quoted = append(quoted, fmt.Sprintf("%q", f))
	}
	summaryJSON := ""
	if summary != "" {
		summaryJSON = fmt.Sprintf(`,"summary":{"intent":%q,"outcome":"done","open_items":["update docs"]}`, summary)
	}
	session := fmt.Sprintf(`{"checkpoint_id":%q,"agent":"Claude Code","model":"test-model","created_at":"2026-09-06T10:00:00Z","files_touched":[%s]%s}`,
		id, strings.Join(quoted, ","), summaryJSON)
	root := fmt.Sprintf(`{"checkpoint_id":%q,"sessions":[{"metadata":"/1/metadata.json","prompt":"/1/prompt.txt"}]}`, id)

	rootBlob := gitInput(t, repo, root, "hash-object", "-w", "--stdin")
	sessionBlob := gitInput(t, repo, session, "hash-object", "-w", "--stdin")
	promptBlob := gitInput(t, repo, prompt, "hash-object", "-w", "--stdin")
	sessionTree := gitInput(t, repo, fmt.Sprintf("100644 blob %s\tmetadata.json\n100644 blob %s\tprompt.txt\n", sessionBlob, promptBlob), "mktree")
	return gitInput(t, repo, fmt.Sprintf("100644 blob %s\tmetadata.json\n040000 tree %s\t1\n", rootBlob, sessionTree), "mktree")
}

// writeFakeCheckpoint stores a checkpoint the way the refs store does: one ref
// per checkpoint, sharded by the last two characters of its id.
func writeFakeCheckpoint(t *testing.T, repo, id, summary, prompt string, filesTouched []string) {
	t.Helper()
	tree := fakeCheckpointTree(t, repo, id, summary, prompt, filesTouched)
	commit := gitInput(t, repo, "", "commit-tree", tree, "-m", "checkpoint "+id)
	shard := id[len(id)-2:]
	git(t, repo, "update-ref", "refs/entire/checkpoints/"+shard+"/"+id, commit)
}

// writeFakeCheckpointV1 stores a checkpoint the way the shared
// entire/checkpoints/v1 branch does: under <first two chars>/<rest of id>/ in
// one tree for the whole repository.
func writeFakeCheckpointV1(t *testing.T, repo, id, summary, prompt string, filesTouched []string) {
	t.Helper()
	tree := fakeCheckpointTree(t, repo, id, summary, prompt, filesTouched)
	rest := gitInput(t, repo, fmt.Sprintf("040000 tree %s\t%s\n", tree, id[2:]), "mktree")
	shard := gitInput(t, repo, fmt.Sprintf("040000 tree %s\t%s\n", rest, id[:2]), "mktree")
	commit := gitInput(t, repo, "", "commit-tree", shard, "-m", "checkpoints v1")
	git(t, repo, "update-ref", "refs/heads/entire/checkpoints/v1", commit)
}

func TestCheckpointIntentsReadsTheSharedV1Branch(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})

	// Mirror-connected repositories keep their checkpoints on one shared
	// branch with 12-character ids sharded by the first two characters.
	const id = "17790384bf09"
	writeFakeCheckpointV1(t, repo, id, "Convert pricing to rupees", "please make Price return rupees", []string{"pricing.go"})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees", "Entire-Checkpoint: "+id)

	got, err := CheckpointIntents(context.Background(), repo, "main", "rupees")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != IntentSourceSummary || got[0].Intent != "Convert pricing to rupees" || got[0].CheckpointID != id {
		t.Fatalf("v1 branch intent wrong: %+v", got)
	}

	// A checkpoint in neither store is reported, and the note names both.
	radarBranch(t, repo, "orphan", map[string]string{"invoice.go": radarInvoicing}, "invoicing", "Entire-Checkpoint: deadbeef0000")
	orphan, err := CheckpointIntents(context.Background(), repo, "main", "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if len(orphan) != 1 || orphan[0].Source != IntentSourceMissing || !strings.Contains(orphan[0].Note, "v1") {
		t.Fatalf("orphan checkpoint should be missing with a note naming both stores, got %+v", orphan)
	}
}

func TestCheckpointIntentsReadsSummaryAndFallsBackToPrompt(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})

	const summarised = "01TESTCKPTSUMMARY0000000AB"
	writeFakeCheckpoint(t, repo, summarised, "Convert pricing to rupees", "please make Price return rupees", []string{"pricing.go"})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees", "Entire-Checkpoint: "+summarised)

	const promptOnly = "01TESTCKPTPROMPTONLY00000CD"
	writeFakeCheckpoint(t, repo, promptOnly, "", "Build invoicing that totals Price() which is in paise", []string{"invoice.go"})
	radarBranch(t, repo, "invoicing", map[string]string{"invoice.go": radarInvoicing}, "invoicing on paise", "Entire-Checkpoint: "+promptOnly)

	radarBranch(t, repo, "plain", map[string]string{"invoice.go": radarBaseInvoice + "\nfunc Footer() string { return \"\" }\n"}, "no checkpoint here")

	a, err := CheckpointIntents(context.Background(), repo, "main", "rupees")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].Source != IntentSourceSummary || a[0].Intent != "Convert pricing to rupees" || a[0].Agent != "Claude Code" {
		t.Fatalf("summary intent wrong: %+v", a)
	}
	if len(a[0].FilesTouched) != 1 || a[0].FilesTouched[0] != "pricing.go" || len(a[0].OpenItems) != 1 {
		t.Fatalf("summary metadata wrong: %+v", a[0])
	}

	b, err := CheckpointIntents(context.Background(), repo, "main", "invoicing")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b[0].Source != IntentSourcePrompt || !strings.Contains(b[0].Intent, "paise") || b[0].Note == "" {
		t.Fatalf("prompt fallback wrong: %+v", b)
	}

	plain, err := CheckpointIntents(context.Background(), repo, "main", "plain")
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 1 || plain[0].Source != IntentSourceMissing || plain[0].CheckpointID != "" || plain[0].Note == "" {
		t.Fatalf("missing trailer should be reported, got %+v", plain)
	}

	report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "invoicing", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := AttachIntent(context.Background(), repo, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", report.Conflicts)
	}
	c := report.Conflicts[0]
	if c.ChangedIntent == nil || c.ChangedIntent.Intent != "Convert pricing to rupees" || c.ChangedIntent.Match != "files_touched" {
		t.Fatalf("changed intent wrong: %+v", c.ChangedIntent)
	}
	if c.ConsumerIntent == nil || c.ConsumerIntent.Source != IntentSourcePrompt || c.ConsumerIntent.Match != "files_touched" {
		t.Fatalf("consumer intent wrong: %+v", c.ConsumerIntent)
	}
}

func TestTruncateRunesMarksTheCut(t *testing.T) {
	short := truncateRunes("hello", 10)
	if short != "hello" {
		t.Fatalf("short string altered: %q", short)
	}
	long := truncateRunes(strings.Repeat("ab", 400), 600)
	if !strings.HasSuffix(long, " [...]") || len(long) > 606 {
		t.Fatalf("long string not truncated cleanly: len=%d suffix=%q", len(long), long[len(long)-6:])
	}
}

// radarCart defines a second entity called Price: a method on Cart. The
// reference index matches by short name, so a caller of Price(item) also
// matches this one, and the graph alone cannot say which Price the caller
// meant.
const radarCart = "package shop\n\ntype Cart struct {\n\titems []string\n}\n\n// Price returns the cart total in paise.\nfunc (c Cart) Price() int {\n\treturn len(c.items) * 100\n}\n"

func TestAnalyzeMergeMarksAmbiguousNameHeuristic(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "cart.go": radarCart, "invoice.go": radarBaseInvoice})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")
	radarBranch(t, repo, "invoicing", map[string]string{"invoice.go": radarInvoicing}, "invoicing on paise")

	report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "invoicing", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d: %+v", len(report.Conflicts), report.Conflicts)
	}
	c := report.Conflicts[0]
	if c.Confidence != ConfidencePotential {
		t.Fatalf("confidence must stay potential, got %q", c.Confidence)
	}
	if c.Evidence != EvidenceHeuristic {
		t.Fatalf("a short-name hit with two definitions must be heuristic, got %q", c.Evidence)
	}
	t.Logf("evidence notes: %v", c.EvidenceNotes)
	if len(c.EvidenceNotes) != 1 || !strings.Contains(c.EvidenceNotes[0], "ambiguous name: 2 definitions of Price") {
		t.Fatalf("evidence notes should explain the ambiguity, got %v", c.EvidenceNotes)
	}
}

func TestAnalyzeMergeReportsPartialAnalysis(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")

	// A fully parsed pair with nothing in common is complete.
	footer := radarBaseInvoice + "\nfunc Footer() string {\n\treturn \"thanks\"\n}\n"
	radarBranch(t, repo, "footer", map[string]string{"invoice.go": footer}, "add footer")
	complete, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "footer", 2)
	if err != nil {
		t.Fatal(err)
	}
	if complete.Analysis.Status != AnalysisComplete || len(complete.Analysis.Reasons) != 0 {
		t.Fatalf("fully parsed branches should be complete, got %+v", complete.Analysis)
	}

	// A file the parser cannot handle on one side makes the analysis partial
	// even when no conflict is found: the unparsed file could hold the caller.
	broken := footer + "\nfunc Total(items []string) int {\n\treturn Price(\n"
	radarBranch(t, repo, "broken", map[string]string{"invoice.go": broken}, "half-written total")
	partial, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "broken", 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("partial reasons: %v", partial.Analysis.Reasons)
	if partial.Analysis.Status != AnalysisPartial || len(partial.Analysis.Reasons) == 0 {
		t.Fatalf("an unparsed file should make the analysis partial, got %+v", partial.Analysis)
	}
	if !strings.Contains(strings.Join(partial.Analysis.Reasons, "\n"), "invoice.go") {
		t.Fatalf("reasons should name the file that could not be parsed, got %v", partial.Analysis.Reasons)
	}
	t.Logf("partial summary: %s", partial.Analysis.Summary)
	if !strings.Contains(partial.Analysis.Summary, "1 file(s) failed to parse") || !strings.Contains(partial.Analysis.Summary, "1 changed file(s) affected") {
		t.Fatalf("summary should count parse failures and affected changed files, got %q", partial.Analysis.Summary)
	}

	// Generated code is edited at its source, not in the tree: a changed
	// generated file means the real change is invisible.
	generated := "// Code generated by pricegen. DO NOT EDIT.\n\n" + radarRupees
	radarBranch(t, repo, "generated", map[string]string{"pricing.go": generated}, "regenerate pricing")
	gen, err := AnalyzeMerge(context.Background(), repo, "main", "generated", "footer", 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("generated reasons: %v", gen.Analysis.Reasons)
	if gen.Analysis.Status != AnalysisPartial || !strings.Contains(strings.Join(gen.Analysis.Reasons, "\n"), "generated") {
		t.Fatalf("a changed generated file should make the analysis partial, got %+v", gen.Analysis)
	}

	// reflect lets code call names the graph never sees as references.
	reflective := footer + "\nfunc Dynamic() int {\n\treturn 0\n}\n"
	reflective = strings.Replace(reflective, "package shop\n", "package shop\n\nimport \"reflect\"\n\nvar _ = reflect.TypeOf\n", 1)
	radarBranch(t, repo, "reflective", map[string]string{"invoice.go": reflective}, "call by name")
	ref, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "reflective", 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reflect reasons: %v", ref.Analysis.Reasons)
	if ref.Analysis.Status != AnalysisPartial || !strings.Contains(strings.Join(ref.Analysis.Reasons, "\n"), "reflect") {
		t.Fatalf("a changed file importing reflect should make the analysis partial, got %+v", ref.Analysis)
	}
}

func TestAnalyzeMergeIsDeterministicWhenTwoOriginsShareAHop(t *testing.T) {
	// Price and Tax are both changed on A and both feed Subtotal, which B's
	// new Total calls. The walk reaches Total once, and which origin it is
	// attributed to must not depend on map iteration order.
	pricing := "package shop\n\nfunc Price(item string) int {\n\treturn 100\n}\n\nfunc Tax(item string) int {\n\treturn 18\n}\n\nfunc Subtotal(items []string) int {\n\ttotal := 0\n\tfor _, item := range items {\n\t\ttotal += Price(item) + Tax(item)\n\t}\n\treturn total\n}\n"
	repo := radarRepo(t, map[string]string{"pricing.go": pricing, "invoice.go": radarBaseInvoice})
	rupees := strings.Replace(pricing, "return 100", "return 1", 1)
	rupees = strings.Replace(rupees, "return 18", "return 0", 1)
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": rupees}, "pricing in rupees")
	total := radarBaseInvoice + "\nfunc Total(items []string) int {\n\treturn Subtotal(items)\n}\n"
	radarBranch(t, repo, "total", map[string]string{"invoice.go": total}, "add total")

	var first string
	for run := 0; run < 6; run++ {
		report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "total", 2)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(report.Conflicts)
		if err != nil {
			t.Fatal(err)
		}
		got := string(encoded)
		if run == 0 {
			first = got
			if len(report.Conflicts) != 1 || report.Conflicts[0].Changed.Name != "Price" {
				t.Fatalf("expected one finding attributed to the alphabetically first origin Price, got %s", got)
			}
			continue
		}
		if got != first {
			t.Fatalf("run %d differs from run 0:\n%s\n%s", run, got, first)
		}
	}
}

func TestAnalyzeMergeSkipsDocumentsUnlessIncluded(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")
	radarBranch(t, repo, "docs", map[string]string{"PRICING.rst": "Pricing\n=======\n\nCall Price to get the amount in paise.\n"}, "document pricing")

	report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "docs", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 0 {
		t.Fatalf("a document mentioning a name is not a code dependency by default, got %+v", report.Conflicts)
	}

	included, err := AnalyzeMergeWithOptions(context.Background(), repo, "main", "rupees", "docs", 2, MergeOptions{IncludeDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(included.Conflicts) != 1 || included.Conflicts[0].Consumer == nil || included.Conflicts[0].Consumer.Kind != "document" {
		t.Fatalf("with documents included the mention should be reported, got %+v", included.Conflicts)
	}
	c := included.Conflicts[0]
	t.Logf("document evidence notes: %v", c.EvidenceNotes)
	if c.Evidence != EvidenceHeuristic || !strings.Contains(strings.Join(c.EvidenceNotes, "\n"), "document") {
		t.Fatalf("a hop through a document is heuristic and must say so, got %q %v", c.Evidence, c.EvidenceNotes)
	}
}

func TestAnalyzeMergeMarksUnparsedFileHeuristic(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")
	// Total is intact; the syntax error is further down the same file, so the
	// parser recovers Total but the file as a whole did not parse.
	broken := radarBaseInvoice + "\nfunc Total(items []string) int {\n\treturn Price(items[0])\n}\n\nfunc Broken( {\n"
	radarBranch(t, repo, "broken", map[string]string{"invoice.go": broken}, "half-written")

	report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "broken", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 1 || report.Conflicts[0].Consumer == nil || report.Conflicts[0].Consumer.Name != "Total" {
		t.Fatalf("expected the recovered Total to be found, got %+v", report.Conflicts)
	}
	c := report.Conflicts[0]
	t.Logf("unparsed evidence notes: %v", c.EvidenceNotes)
	if c.Evidence != EvidenceHeuristic || !strings.Contains(strings.Join(c.EvidenceNotes, "\n"), "file did not parse") {
		t.Fatalf("a finding through an unparsed file must be heuristic and say the file did not parse, got %q %v", c.Evidence, c.EvidenceNotes)
	}
	if report.Analysis.Status != AnalysisPartial {
		t.Fatalf("analysis should be partial, got %+v", report.Analysis)
	}
}

func TestVerifyMergeReproducesTheBreak(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")
	invoiceTest := "package shop\n\nimport \"testing\"\n\nfunc TestInvoice(t *testing.T) {\n\tif got := Invoice([]string{\"a\", \"b\"}); got != 200 {\n\t\tt.Fatalf(\"expected 200 paise, got %d\", got)\n\t}\n}\n"
	radarBranch(t, repo, "invoicing", map[string]string{"invoice.go": radarInvoicing, "invoice_test.go": invoiceTest}, "invoicing on paise")

	v, err := VerifyMerge(context.Background(), repo, "rupees", "invoicing")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("verification: %+v", v)
	if v.Status != VerificationReproduced {
		t.Fatalf("the merged tree does not compile, so the tests must fail: got %+v", v)
	}
	if v.Runner != "go test ./..." || len(v.Output) == 0 || v.Tree == "" {
		t.Fatalf("a reproduced break must carry the runner, the tree and the failing output, got %+v", v)
	}
	if out, _ := exec.Command("git", "-C", repo, "worktree", "list").Output(); strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
		t.Fatalf("scratch worktree was not removed:\n%s", out)
	}
}

func TestVerifyMergePassesWhenTestsStillPass(t *testing.T) {
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing, "invoice.go": radarBaseInvoice})
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees}, "pricing in rupees")
	footer := radarBaseInvoice + "\nfunc Footer() string {\n\treturn \"thanks\"\n}\n"
	footerTest := "package shop\n\nimport \"testing\"\n\nfunc TestFooter(t *testing.T) {\n\tif Footer() != \"thanks\" {\n\t\tt.Fatal(\"footer\")\n\t}\n}\n"
	radarBranch(t, repo, "footer", map[string]string{"invoice.go": footer, "invoice_test.go": footerTest}, "add footer")

	v, err := VerifyMerge(context.Background(), repo, "rupees", "footer")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != VerificationPassed || !strings.Contains(v.Note, "potential") {
		t.Fatalf("passing tests keep the findings potential and must say so, got %+v", v)
	}
}

func TestVerifyMergeUnavailableWithoutRunner(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, "pricing.py", "def price(item):\n    return 100\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "base")
	git(t, repo, "branch", "-M", "main")
	radarBranch(t, repo, "rupees", map[string]string{"pricing.py": "def price(item):\n    return 1.0\n"}, "rupees")
	radarBranch(t, repo, "invoicing", map[string]string{"invoice.py": "from pricing import price\n\ndef invoice(items):\n    return sum(price(i) for i in items)\n"}, "invoicing")

	v, err := VerifyMerge(context.Background(), repo, "rupees", "invoicing")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != VerificationUnavailable || !strings.Contains(v.Note, "no supported test runner") {
		t.Fatalf("a repository without a known test runner must say verification is unavailable, got %+v", v)
	}
	if len(v.Manual) == 0 || !strings.Contains(strings.Join(v.Manual, "\n"), "git merge-tree --write-tree rupees invoicing") {
		t.Fatalf("the manual path must be printed, got %+v", v.Manual)
	}
}

func TestDescribeRefsCapsTheList(t *testing.T) {
	refs := []EntityRef{
		{Path: "a.go", Kind: "function", Name: "op"},
		{Path: "b.go", Kind: "method", Name: "T.op"},
		{Path: "c.go", Kind: "field", Name: "S.op"},
		{Path: "d.go", Kind: "function", Name: "op"},
		{Path: "e.go", Kind: "function", Name: "op"},
	}
	got := describeRefs(refs)
	if !strings.HasSuffix(got, "and 2 more") || strings.Contains(got, "d.go") {
		t.Fatalf("long definition lists must be capped at three with a count, got %q", got)
	}
	if short := describeRefs(refs[:2]); strings.Contains(short, "more") {
		t.Fatalf("short lists are printed in full, got %q", short)
	}
}

func TestAnalyzeMergeIgnoresIdenticalChangesOnBothSides(t *testing.T) {
	// One branch contains the other's commit (or both agents wrote the same
	// file byte for byte). Git merges that cleanly and only one text exists,
	// so it is not a place where two intents collide.
	repo := radarRepo(t, map[string]string{"pricing.go": radarBasePricing})
	note := "Pricing\n=======\n\nPrices are now in rupees.\n"
	radarBranch(t, repo, "rupees", map[string]string{"pricing.go": radarRupees, "NOTE.rst": note}, "pricing in rupees")
	radarBranch(t, repo, "rupees-plus-docs", map[string]string{"pricing.go": radarRupees, "NOTE.rst": note, "README.rst": "Shop\n====\n"}, "same change plus a readme")

	report, err := AnalyzeMerge(context.Background(), repo, "main", "rupees", "rupees-plus-docs", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Conflicts) != 0 {
		t.Fatalf("identical changes on both sides are not conflicts, got %+v", report.Conflicts)
	}
}
