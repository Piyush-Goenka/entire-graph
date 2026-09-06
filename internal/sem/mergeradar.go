package sem

import (
	"context"
	"fmt"
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
	SchemaVersion string            `json:"schema_version"`
	Base          string            `json:"base"`
	A             MergeSide         `json:"a"`
	B             MergeSide         `json:"b"`
	Conflicts     []MergeConflict   `json:"conflicts"`
	Warnings      []ProviderWarning `json:"warnings,omitempty"`
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

	report.Conflicts = append(report.Conflicts, directConflicts(changedA, changedB, refA, refB)...)

	ab, warnings, err := dependencyConflicts(ctx, repo, refA, refB, changedA, changedB, maxDepth)
	if err != nil {
		return MergeReport{}, err
	}
	report.Warnings = append(report.Warnings, warnings...)
	report.Conflicts = append(report.Conflicts, ab...)

	ba, warnings, err := dependencyConflicts(ctx, repo, refB, refA, changedB, changedA, maxDepth)
	if err != nil {
		return MergeReport{}, err
	}
	report.Warnings = append(report.Warnings, warnings...)
	report.Conflicts = append(report.Conflicts, ba...)

	report.Conflicts = dedupeConflicts(report.Conflicts)
	sortConflicts(report.Conflicts)
	return report, nil
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
func dependencyConflicts(ctx context.Context, repo, changedRef, consumerRef string, changed, consumerChanged map[string][]ChangedEntity, maxDepth int) ([]MergeConflict, []ProviderWarning, error) {
	frontier := map[string]hop{}
	visited := map[string]bool{}
	for name, ents := range changed {
		frontier[name] = hop{origin: ents[0]}
		visited[name] = true
	}
	var out []MergeConflict
	var warnings []ProviderWarning
	for depth := 1; depth <= maxDepth && len(frontier) > 0; depth++ {
		names := make(map[string]struct{}, len(frontier))
		for name := range frontier {
			names[name] = struct{}{}
		}
		refs, scanWarnings, err := ReferencesTo(ctx, repo, consumerRef, names)
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, scanWarnings...)
		next := map[string]hop{}
		for name, h := range frontier {
			for _, ref := range refs[name] {
				short := shortEntityName(ref.Name)
				if ents, ok := consumerChanged[short]; ok {
					for _, ent := range ents {
						if ent.Path != ref.Path || ent.Type == "removed" {
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
