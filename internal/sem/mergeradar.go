package sem

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// MergeRadarSchemaVersion pins the shape of MergeReport.
const MergeRadarSchemaVersion = "merge-radar/1"

// ConfidencePotential labels a conflict the graph inferred from references
// but that no merged tree has been tested against. It is the only confidence
// this analysis emits on its own: upgrading a finding requires running code.
const ConfidencePotential = "potential"

// Evidence classes say how much the graph actually proved about a finding.
// Structural means every hop in the chain resolved to exactly one parsed
// entity with no coverage warning on its file. Heuristic means the chain was
// matched by short name where that name has several definitions, passes
// through a method that may dispatch dynamically, or crosses a file the
// analysis could not fully parse. Neither class is a verified break: the
// confidence stays potential until a merged tree is tested.
const (
	EvidenceStructural = "structural"
	EvidenceHeuristic  = "heuristic"
)

// Analysis coverage. Complete means every changed file and every file the
// reference walk touched was parsed and every matched name resolved to one
// definition. Partial means at least one of those failed, so an empty
// conflict list is not evidence of safety.
const (
	AnalysisComplete = "complete"
	AnalysisPartial  = "partial"
)

// MergeAnalysis states how much of the two branches the analysis could see.
// Reasons name what it could not: unparsed or unsupported files, ambiguous
// names, generated code and reflection.
type MergeAnalysis struct {
	Status  string   `json:"status"`
	Summary string   `json:"summary,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

// Conflict kinds. Direct means both branches changed the same entity.
// Dependency means one branch changed an entity that code changed or added on
// the other branch references, so neither branch's tests could have exercised
// the combination.
const (
	ConflictDirect     = "direct"
	ConflictDependency = "dependency"
)

// EntityRef identifies one parsed entity in a tree by file, kind and name.
type EntityRef struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// ChangedEntity is one entity-level change on a branch, with enough context
// to explain a conflict.
type ChangedEntity struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	OldSignature string `json:"old_signature,omitempty"`
	NewSignature string `json:"new_signature,omitempty"`
	Line         int    `json:"line,omitempty"`
}

// MergeSide summarizes one branch's semantic diff against the base, plus the
// checkpoint intent behind its commits when AttachIntent has run.
type MergeSide struct {
	Ref      string             `json:"ref"`
	Commit   string             `json:"commit"`
	Files    int                `json:"files_changed"`
	Entities int                `json:"entities_changed"`
	Intents  []CheckpointIntent `json:"intents,omitempty"`
}

// MergeConflict is one place where the two branches' changes meet. Via lists
// the intermediate entities when the reference is indirect. The two intent
// pointers are populated by AttachIntent.
type MergeConflict struct {
	Kind           string            `json:"kind"`
	Confidence     string            `json:"confidence"`
	Evidence       string            `json:"evidence"`
	EvidenceNotes  []string          `json:"evidence_notes,omitempty"`
	Changed        ChangedEntity     `json:"changed"`
	ChangedOn      string            `json:"changed_on"`
	Consumer       *ChangedEntity    `json:"consumer,omitempty"`
	ConsumerOn     string            `json:"consumer_on,omitempty"`
	Via            []EntityRef       `json:"via,omitempty"`
	Explanation    string            `json:"explanation"`
	ChangedIntent  *CheckpointIntent `json:"changed_intent,omitempty"`
	ConsumerIntent *CheckpointIntent `json:"consumer_intent,omitempty"`
}

// MergeReport is the full result of AnalyzeMerge.
type MergeReport struct {
	SchemaVersion string             `json:"schema_version"`
	Base          string             `json:"base"`
	A             MergeSide          `json:"a"`
	B             MergeSide          `json:"b"`
	Analysis      MergeAnalysis      `json:"analysis"`
	Conflicts     []MergeConflict    `json:"conflicts"`
	Verification  *MergeVerification `json:"verification,omitempty"`
	Warnings      []ProviderWarning  `json:"warnings,omitempty"`
}

// ChangedNames returns the short reference names of every entity changed in
// a semantic diff result: the same set the dependents scan resolves.
func ChangedNames(result Result) map[string]struct{} {
	return changedReferenceNames(result)
}

// ReferencesTo scans the tree at head for entities whose bodies mention any
// of names as an exact identifier token, grouped by name. It reuses the
// dependents reference index, so its notion of a reference is identical to
// the one behind dependents_count.
func ReferencesTo(ctx context.Context, repo, head string, names map[string]struct{}) (map[string][]EntityRef, []ProviderWarning, error) {
	index, warnings, err := buildReferenceIndex(ctx, repo, head, names)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string][]EntityRef, len(index))
	for name, keys := range index {
		refs := make([]EntityRef, 0, len(keys))
		for key := range keys {
			if ref, ok := parseEntityKey(key); ok {
				refs = append(refs, ref)
			}
		}
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].Path != refs[j].Path {
				return refs[i].Path < refs[j].Path
			}
			if refs[i].Kind != refs[j].Kind {
				return refs[i].Kind < refs[j].Kind
			}
			return refs[i].Name < refs[j].Name
		})
		out[name] = refs
	}
	return out, warnings, nil
}

// parseEntityKey inverts the dependents scan's "path#kind:name" key.
func parseEntityKey(key string) (EntityRef, bool) {
	hash := strings.Index(key, "#")
	if hash < 0 {
		return EntityRef{}, false
	}
	rest := key[hash+1:]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return EntityRef{}, false
	}
	return EntityRef{Path: key[:hash], Kind: rest[:colon], Name: rest[colon+1:]}, true
}

// AnalyzeMerge compares refA and refB against base and cross-references the
// two semantic diffs. maxDepth bounds how many reference hops to follow from
// a changed entity when looking for changed code on the other branch.
func AnalyzeMerge(ctx context.Context, repo, base, refA, refB string, maxDepth int) (MergeReport, error) {
	return AnalyzeMergeWithOptions(ctx, repo, base, refA, refB, maxDepth, MergeOptions{})
}

// MergeOptions tunes what the reference walk may pass through.
type MergeOptions struct {
	// IncludeDocs lets document entities (Markdown, reStructuredText and
	// similar prose) take part in the walk. Off by default: a document that
	// mentions a name is not a code dependency, and on a large repository
	// those mentions drown the structural findings.
	IncludeDocs bool
}

// AnalyzeMergeWithOptions is AnalyzeMerge with the walk options exposed.
func AnalyzeMergeWithOptions(ctx context.Context, repo, base, refA, refB string, maxDepth int, opts MergeOptions) (MergeReport, error) {
	if maxDepth < 1 {
		maxDepth = 1
	}
	resA, err := AnalyzeGitRange(ctx, repo, base, refA, nil)
	if err != nil {
		return MergeReport{}, fmt.Errorf("analyze %s..%s: %w", base, refA, err)
	}
	resB, err := AnalyzeGitRange(ctx, repo, base, refB, nil)
	if err != nil {
		return MergeReport{}, fmt.Errorf("analyze %s..%s: %w", base, refB, err)
	}
	sideA, err := summarizeSide(ctx, repo, refA, resA)
	if err != nil {
		return MergeReport{}, err
	}
	sideB, err := summarizeSide(ctx, repo, refB, resB)
	if err != nil {
		return MergeReport{}, err
	}
	report := MergeReport{SchemaVersion: MergeRadarSchemaVersion, Base: base, A: sideA, B: sideB}
	report.Warnings = append(append([]ProviderWarning{}, resA.Warnings...), resB.Warnings...)

	changedA := indexChanged(resA)
	changedB := indexChanged(resB)

	direct, err := dropIdenticalFiles(ctx, repo, refA, refB, directConflicts(changedA, changedB, refA, refB))
	if err != nil {
		return MergeReport{}, err
	}
	report.Conflicts = append(report.Conflicts, direct...)

	ab, warnings, err := dependencyConflicts(ctx, repo, refA, refB, changedA, changedB, maxDepth, opts)
	if err != nil {
		return MergeReport{}, err
	}
	report.Warnings = append(report.Warnings, warnings...)
	report.Conflicts = append(report.Conflicts, ab...)

	ba, warnings, err := dependencyConflicts(ctx, repo, refB, refA, changedB, changedA, maxDepth, opts)
	if err != nil {
		return MergeReport{}, err
	}
	report.Warnings = append(report.Warnings, warnings...)
	report.Conflicts = append(report.Conflicts, ba...)

	report.Conflicts = dedupeConflicts(report.Conflicts)
	sortConflicts(report.Conflicts)
	ambiguous, err := classifyConflicts(ctx, repo, report.Conflicts, report.Warnings)
	if err != nil {
		return MergeReport{}, err
	}
	report.Analysis, err = assessCoverage(ctx, repo, report.Warnings, ambiguous, []sideFiles{{ref: refA, files: resA.Files}, {ref: refB, files: resB.Files}})
	if err != nil {
		return MergeReport{}, err
	}
	return report, nil
}

type sideFiles struct {
	ref   string
	files []FileChange
}

var (
	generatedHeader = regexp.MustCompile(`(?i)Code generated .*DO NOT EDIT|@generated\b`)
	reflectImport   = regexp.MustCompile(`(?m)^\s*(import\s+)?"reflect"\s*$`)
)

// assessCoverage derives the report-level coverage from what the analysis
// already knows it missed: provider warnings on files in the diff or the
// reference walk, names that matched more than one definition, and changed
// files whose real logic lives outside the tree (generated code) or is
// invoked by name at runtime (reflect).
func assessCoverage(ctx context.Context, repo string, warnings []ProviderWarning, ambiguous []string, sides []sideFiles) (MergeAnalysis, error) {
	var reasons []string

	filesByCode := map[string][]string{}
	seenFile := map[string]bool{}
	var codes []string
	for _, w := range warnings {
		key := w.Code + "\x00" + w.FilePath
		if seenFile[key] {
			continue
		}
		seenFile[key] = true
		if _, ok := filesByCode[w.Code]; !ok {
			codes = append(codes, w.Code)
		}
		if w.FilePath != "" {
			filesByCode[w.Code] = append(filesByCode[w.Code], w.FilePath)
		} else {
			filesByCode[w.Code] = append(filesByCode[w.Code], "("+w.EffectOnCompleteness+")")
		}
	}
	for _, code := range codes {
		files := filesByCode[code]
		shown := files
		more := ""
		if len(shown) > 3 {
			shown = shown[:3]
			more = fmt.Sprintf(" and %d more", len(files)-3)
		}
		reasons = append(reasons, fmt.Sprintf("%s: %d file(s) not fully analysed (%s%s); references inside them are invisible", code, len(files), strings.Join(shown, ", "), more))
	}

	reasons = append(reasons, ambiguous...)

	changed := map[string]bool{}
	for _, side := range sides {
		for _, file := range side.files {
			changed[file.Path] = true
		}
	}
	affected := map[string]bool{}
	parseFailed, unsupported, other := 0, 0, 0
	for code, files := range filesByCode {
		for _, f := range files {
			if changed[f] {
				affected[f] = true
			}
		}
		switch code {
		case "E_PARSE_ERROR", "E_PARSE_TIMEOUT", "E_PARSE_DEPTH_EXCEEDED":
			parseFailed += len(files)
		case "E_UNSUPPORTED_LANGUAGE", "W_UNSUPPORTED_FILE":
			unsupported += len(files)
		default:
			other += len(files)
		}
	}
	special := 0

	for _, side := range sides {
		for _, file := range side.files {
			if file.Status == "deleted" || file.Status == "removed" || file.Status == "D" {
				continue
			}
			content, ok, err := gitutil.ShowFile(ctx, repo, side.ref, file.Path)
			if err != nil {
				return MergeAnalysis{}, err
			}
			if !ok {
				continue
			}
			head := content
			if len(head) > 4096 {
				head = head[:4096]
			}
			if generatedHeader.MatchString(head) {
				reasons = append(reasons, fmt.Sprintf("generated code: %s on %s carries a Code generated header; the real change is in its generator, which the graph does not see", file.Path, side.ref))
				affected[file.Path] = true
				special++
			}
			if strings.HasSuffix(file.Path, ".go") && reflectImport.MatchString(head) {
				reasons = append(reasons, fmt.Sprintf("reflect: %s on %s imports reflect; calls made by name at runtime never appear as references", file.Path, side.ref))
				affected[file.Path] = true
				special++
			}
		}
	}

	if len(reasons) == 0 {
		return MergeAnalysis{Status: AnalysisComplete}, nil
	}
	var parts []string
	if parseFailed > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) failed to parse", parseFailed))
	}
	if unsupported > 0 {
		parts = append(parts, fmt.Sprintf("%d unsupported", unsupported))
	}
	if other > 0 {
		parts = append(parts, fmt.Sprintf("%d other warning(s)", other))
	}
	if len(ambiguous) > 0 {
		parts = append(parts, fmt.Sprintf("%d ambiguous name(s)", len(ambiguous)))
	}
	if special > 0 {
		parts = append(parts, fmt.Sprintf("%d generated or reflective file(s)", special))
	}
	summary := strings.Join(parts, ", ")
	if len(affected) > 0 {
		summary += fmt.Sprintf("; %d changed file(s) affected", len(affected))
	}
	return MergeAnalysis{Status: AnalysisPartial, Summary: summary, Reasons: reasons}, nil
}

// definitionsAt lists the entities defined in head whose short name is one of
// names. It answers the question the reference index cannot: how many
// different things a short-name hit could have meant. Files the parser
// cannot handle are skipped here; the reference walk already warned about
// them.
func definitionsAt(ctx context.Context, repo, head string, names map[string]struct{}) (map[string][]EntityRef, error) {
	out := map[string][]EntityRef{}
	if len(names) == 0 {
		return out, nil
	}
	files, _, _, err := referenceCandidateFiles(ctx, repo, head, names)
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	parser := TreeSitterParser{}
	for _, path := range files {
		if !Supported(path) {
			continue
		}
		content, ok, err := gitutil.ShowFile(ctx, repo, head, path)
		if err != nil {
			return nil, err
		}
		if !ok || len(content) > defaultMaxParseBytes || !containsAnyName(content, names) {
			continue
		}
		entities, _, _ := parser.ParseWithStatus(path, content)
		for _, entity := range entities {
			short := shortEntityName(entity.Name)
			if _, wanted := names[short]; !wanted {
				continue
			}
			out[short] = append(out[short], EntityRef{Path: path, Kind: entity.Kind, Name: entity.Name})
		}
	}
	return out, nil
}

// matchedNames returns the short names a finding's chain was matched by: the
// changed entity's name, then each intermediate hop's name. The consumer's
// own name is never matched, so it is not included.
func matchedNames(c MergeConflict) []string {
	names := []string{shortEntityName(c.Changed.Name)}
	for _, v := range c.Via {
		names = append(names, shortEntityName(v.Name))
	}
	return names
}

// classifyConflicts assigns an evidence class and its reasons to every
// finding. Definitions are looked up once per consumer branch for all the
// names its findings were matched by.
func classifyConflicts(ctx context.Context, repo string, conflicts []MergeConflict, warnings []ProviderWarning) ([]string, error) {
	namesByHead := map[string]map[string]struct{}{}
	for _, c := range conflicts {
		if c.Kind != ConflictDependency {
			continue
		}
		if namesByHead[c.ConsumerOn] == nil {
			namesByHead[c.ConsumerOn] = map[string]struct{}{}
		}
		for _, name := range matchedNames(c) {
			namesByHead[c.ConsumerOn][name] = struct{}{}
		}
	}
	defsByHead := map[string]map[string][]EntityRef{}
	var ambiguous []string
	heads := make([]string, 0, len(namesByHead))
	for head := range namesByHead {
		heads = append(heads, head)
	}
	sort.Strings(heads)
	for _, head := range heads {
		defs, err := definitionsAt(ctx, repo, head, namesByHead[head])
		if err != nil {
			return nil, err
		}
		defsByHead[head] = defs
		names := make([]string, 0, len(defs))
		for name, found := range defs {
			if len(found) > 1 {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			ambiguous = append(ambiguous, fmt.Sprintf("ambiguous name: %s has %d definitions on %s (%s); findings matched by it are heuristic", name, len(defs[name]), head, describeRefs(defs[name])))
		}
	}
	for i := range conflicts {
		classifyEvidence(&conflicts[i], defsByHead[conflicts[i].ConsumerOn], warnings)
	}
	return ambiguous, nil
}

// describeRefsShown bounds how many definitions a note lists; the count
// carries the rest.
const describeRefsShown = 3

func describeRefs(refs []EntityRef) string {
	names := make([]string, 0, len(refs))
	for i, d := range refs {
		if i == describeRefsShown {
			names = append(names, fmt.Sprintf("and %d more", len(refs)-describeRefsShown))
			break
		}
		names = append(names, d.Kind+" "+d.Name+" in "+d.Path)
	}
	return strings.Join(names, ", ")
}

// classifyEvidence decides how much the graph proved about one finding. A
// note is recorded for each reason the match could be wrong or incomplete;
// with no notes the evidence is structural.
func classifyEvidence(c *MergeConflict, defs map[string][]EntityRef, warnings []ProviderWarning) {
	var notes []string
	chain := []EntityRef{{Path: c.Changed.Path, Kind: c.Changed.Kind, Name: c.Changed.Name}}
	chain = append(chain, c.Via...)
	if c.Consumer != nil {
		chain = append(chain, EntityRef{Path: c.Consumer.Path, Kind: c.Consumer.Kind, Name: c.Consumer.Name})
	}
	switch c.Kind {
	case ConflictDependency:
		for _, name := range matchedNames(*c) {
			if found := defs[name]; len(found) > 1 {
				notes = append(notes, fmt.Sprintf("ambiguous name: %d definitions of %s on %s (%s); the reference index matches by short name and cannot say which one the consumer meant",
					len(found), name, c.ConsumerOn, describeRefs(found)))
			}
		}
		for _, ref := range chain {
			switch ref.Kind {
			case "method":
				notes = append(notes, fmt.Sprintf("method in chain: %s in %s; a call to it may dispatch through an interface the graph cannot resolve", ref.Name, ref.Path))
			case documentKind, "section":
				notes = append(notes, fmt.Sprintf("hop through document: %s in %s; a mention in prose is not a code reference", ref.Name, ref.Path))
			}
		}
	case ConflictDirect:
		if c.Consumer != nil && (c.Consumer.Kind != c.Changed.Kind || c.Consumer.Name != c.Changed.Name) {
			notes = append(notes, fmt.Sprintf("same short name, different entities: %s %s and %s %s in %s", c.Changed.Kind, c.Changed.Name, c.Consumer.Kind, c.Consumer.Name, c.Changed.Path))
		}
	}
	files := map[string]bool{}
	for _, ref := range chain {
		files[ref.Path] = true
	}
	seen := map[string]bool{}
	for _, w := range warnings {
		if w.FilePath == "" || !files[w.FilePath] {
			continue
		}
		key := w.Code + "\x00" + w.FilePath
		if seen[key] {
			continue
		}
		seen[key] = true
		notes = append(notes, fmt.Sprintf("file did not parse: %s (%s); %s", w.FilePath, w.Code, w.EffectOnCompleteness))
	}
	if len(notes) > 0 {
		c.Evidence = EvidenceHeuristic
		c.EvidenceNotes = notes
		return
	}
	c.Evidence = EvidenceStructural
	c.EvidenceNotes = nil
}

func summarizeSide(ctx context.Context, repo, ref string, result Result) (MergeSide, error) {
	commit, err := gitutil.RevParse(ctx, repo, ref)
	if err != nil {
		return MergeSide{}, fmt.Errorf("resolve %s: %w", ref, err)
	}
	side := MergeSide{Ref: ref, Commit: strings.TrimSpace(commit), Files: len(result.Files)}
	for _, file := range result.Files {
		side.Entities += len(file.Changes)
	}
	return side, nil
}

// indexChanged groups a diff's entity changes by short reference name, the
// same key the reference index uses for tokens.
func indexChanged(result Result) map[string][]ChangedEntity {
	out := map[string][]ChangedEntity{}
	for _, file := range result.Files {
		for _, change := range file.Changes {
			name := referenceName(change)
			if name == "" {
				continue
			}
			entity := ChangedEntity{
				Path:         file.Path,
				Kind:         change.Kind,
				Name:         change.Name,
				Type:         change.Type,
				OldSignature: change.OldSignature,
				NewSignature: change.NewSignature,
				Line:         change.AfterStartLine,
			}
			if change.Type == "renamed" && change.NewName != "" {
				entity.Name = change.NewName
			}
			if entity.Line == 0 {
				entity.Line = change.BeforeStartLine
			}
			out[name] = append(out[name], entity)
		}
	}
	return out
}

func directConflicts(changedA, changedB map[string][]ChangedEntity, refA, refB string) []MergeConflict {
	var out []MergeConflict
	for name, entsA := range changedA {
		entsB, ok := changedB[name]
		if !ok {
			continue
		}
		for _, a := range entsA {
			for _, b := range entsB {
				if a.Path != b.Path {
					continue
				}
				consumer := b
				out = append(out, MergeConflict{
					Kind:       ConflictDirect,
					Confidence: ConfidencePotential,
					Changed:    a,
					ChangedOn:  refA,
					Consumer:   &consumer,
					ConsumerOn: refB,
					Explanation: fmt.Sprintf(
						"both branches changed %s %s in %s (%s on %s, %s on %s); Git may merge the text, but only one of the two intents can survive",
						a.Kind, name, a.Path, a.Type, refA, b.Type, refB),
				})
			}
		}
	}
	return out
}

// documentKind is the entity kind the parser gives whole prose files
// (reStructuredText, HTML); Markdown headings are "section". A mention in
// either is not a code reference.
const documentKind = "document"

func isProseKind(kind string) bool {
	return kind == documentKind || kind == "section"
}

// firstCodeEntity returns the first entity in ents the walk may start from.
func firstCodeEntity(ents []ChangedEntity, skip func(kind string) bool) (ChangedEntity, bool) {
	for _, ent := range ents {
		if !skip(ent.Kind) {
			return ent, true
		}
	}
	return ChangedEntity{}, false
}

// dropIdenticalFiles removes direct conflicts on files whose blobs are the
// same on both branches: one branch contains the other's commit, or both
// agents wrote the same bytes. Git merges that without choosing, so there is
// no collision of intents to report.
func dropIdenticalFiles(ctx context.Context, repo, refA, refB string, conflicts []MergeConflict) ([]MergeConflict, error) {
	identical := map[string]bool{}
	out := conflicts[:0]
	for _, c := range conflicts {
		same, seen := identical[c.Changed.Path]
		if !seen {
			blobA, errA := gitutil.RevParse(ctx, repo, refA+":"+c.Changed.Path)
			blobB, errB := gitutil.RevParse(ctx, repo, refB+":"+c.Changed.Path)
			same = errA == nil && errB == nil && blobA == blobB
			identical[c.Changed.Path] = same
		}
		if same {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// hop carries the originating change and the chain of intermediate entities
// through which a name was reached during the reference walk.
type hop struct {
	origin ChangedEntity
	via    []EntityRef
}

// dependencyConflicts walks references in consumerRef's tree outward from the
// entities changed on changedRef. A referencing entity that consumerRef also
// changed or added is a conflict; every referencing entity is a candidate for
// the next hop regardless, so an untouched intermediate caller still connects
// a changed entity to changed code two hops away.
//
// The walk is deterministic: frontier names are visited in sorted order, so
// when two origins reach the same intermediate the finding is always
// attributed to the same one and the report never changes between runs.
// Document entities are skipped unless opts.IncludeDocs is set.
func dependencyConflicts(ctx context.Context, repo, changedRef, consumerRef string, changed, consumerChanged map[string][]ChangedEntity, maxDepth int, opts MergeOptions) ([]MergeConflict, []ProviderWarning, error) {
	skip := func(kind string) bool { return !opts.IncludeDocs && isProseKind(kind) }
	frontier := map[string]hop{}
	visited := map[string]bool{}
	for name, ents := range changed {
		origin, ok := firstCodeEntity(ents, skip)
		if !ok {
			continue
		}
		frontier[name] = hop{origin: origin}
		visited[name] = true
	}
	var out []MergeConflict
	var warnings []ProviderWarning
	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		names := make(map[string]struct{}, len(frontier))
		ordered := make([]string, 0, len(frontier))
		for name := range frontier {
			names[name] = struct{}{}
			ordered = append(ordered, name)
		}
		sort.Strings(ordered)
		refs, scanWarnings, err := ReferencesTo(ctx, repo, consumerRef, names)
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, scanWarnings...)
		next := map[string]hop{}
		for _, name := range ordered {
			h := frontier[name]
			for _, ref := range refs[name] {
				if skip(ref.Kind) {
					continue
				}
				short := shortEntityName(ref.Name)
				if ents, ok := consumerChanged[short]; ok {
					for _, ent := range ents {
						if ent.Path != ref.Path || ent.Type == "removed" || skip(ent.Kind) {
							continue
						}
						consumer := ent
						out = append(out, MergeConflict{
							Kind:        ConflictDependency,
							Confidence:  ConfidencePotential,
							Changed:     h.origin,
							ChangedOn:   changedRef,
							Consumer:    &consumer,
							ConsumerOn:  consumerRef,
							Via:         append([]EntityRef(nil), h.via...),
							Explanation: explainDependency(changedRef, consumerRef, h.origin, ent, h.via),
						})
					}
				}
				if visited[short] {
					continue
				}
				visited[short] = true
				next[short] = hop{origin: h.origin, via: append(append([]EntityRef(nil), h.via...), ref)}
			}
		}
		frontier = next
	}
	return out, warnings, nil
}

func explainDependency(changedRef, consumerRef string, changed, consumer ChangedEntity, via []EntityRef) string {
	viaText := ""
	if len(via) > 0 {
		names := make([]string, 0, len(via))
		for _, v := range via {
			names = append(names, shortEntityName(v.Name))
		}
		viaText = " through " + strings.Join(names, " -> ")
	}
	return fmt.Sprintf(
		"%s changed %s %s in %s (%s); %s %s %s %s in %s, which references it%s. Each branch's tests ran against the other's old code, so this combination has never executed.",
		changedRef, changed.Kind, changed.Name, changed.Path, changed.Type,
		consumerRef, consumer.Type, consumer.Kind, consumer.Name, consumer.Path, viaText)
}

func conflictKey(c MergeConflict) string {
	consumer := ""
	if c.Consumer != nil {
		consumer = c.Consumer.Path + "|" + c.Consumer.Name
	}
	return strings.Join([]string{c.Kind, c.ChangedOn, c.Changed.Path, c.Changed.Name, c.ConsumerOn, consumer}, "|")
}

// dedupeConflicts keeps the first (shortest-path) finding for each
// changed/consumer pair; the walk can reach the same pair by several routes.
func dedupeConflicts(conflicts []MergeConflict) []MergeConflict {
	seen := map[string]bool{}
	out := make([]MergeConflict, 0, len(conflicts))
	for _, c := range conflicts {
		key := conflictKey(c)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func sortConflicts(conflicts []MergeConflict) {
	sort.SliceStable(conflicts, func(i, j int) bool {
		a, b := conflicts[i], conflicts[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Changed.Path != b.Changed.Path {
			return a.Changed.Path < b.Changed.Path
		}
		if a.Changed.Name != b.Changed.Name {
			return a.Changed.Name < b.Changed.Name
		}
		return conflictKey(a) < conflictKey(b)
	})
}
