package sem

import (
	"context"
	"fmt"
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
