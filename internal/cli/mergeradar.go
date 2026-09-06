package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

const (
	defaultMergeRadarDepth = 2
	maxMergeRadarDepth     = 3
)

type mergeRadarFlags struct {
	Repo        string
	Base        string
	JSON        bool
	NoIntent    bool
	Verify      bool
	IncludeDocs bool
	Depth       int
	Refs        []string
}

func parseMergeRadarFlags(args []string) (mergeRadarFlags, error) {
	flags := mergeRadarFlags{Depth: defaultMergeRadarDepth}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		switch arg {
		case "--repo":
			item, err := value()
			if err != nil {
				return flags, err
			}
			flags.Repo = item
		case "--base":
			item, err := value()
			if err != nil {
				return flags, err
			}
			flags.Base = item
		case "--depth":
			item, err := value()
			if err != nil {
				return flags, err
			}
			depth, convErr := strconv.Atoi(item)
			if convErr != nil || depth < 1 || depth > maxMergeRadarDepth {
				return flags, fmt.Errorf("--depth must be an integer from 1 to %d, got %q", maxMergeRadarDepth, item)
			}
			flags.Depth = depth
		case "--json":
			flags.JSON = true
		case "--no-intent":
			flags.NoIntent = true
		case "--verify":
			flags.Verify = true
		case "--include-docs":
			flags.IncludeDocs = true
		default:
			if strings.HasPrefix(arg, "-") {
				return flags, fmt.Errorf("unknown flag %s for merge-radar", arg)
			}
			flags.Refs = append(flags.Refs, arg)
		}
	}
	if len(flags.Refs) != 2 {
		return flags, errors.New("merge-radar needs exactly two branches: entire graph merge-radar <branch-a> <branch-b> [--base <ref>]")
	}
	if flags.Refs[0] == flags.Refs[1] {
		return flags, fmt.Errorf("merge-radar needs two different branches, got %q twice", flags.Refs[0])
	}
	return flags, nil
}

func runMergeRadar(ctx context.Context, opts Options, args []string) error {
	flags, err := parseMergeRadarFlags(args)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(ctx, opts.Env, flags.Repo)
	if err != nil {
		return err
	}
	if err := sem.EnsureGitMetadataSafeForSubprocess(repo); err != nil {
		return err
	}
	base := flags.Base
	if base == "" {
		base, err = gitutil.MergeBase(ctx, repo, flags.Refs[0], flags.Refs[1])
		if err != nil {
			return fmt.Errorf("merge-radar: no merge base between %s and %s; pass --base <ref>: %w", flags.Refs[0], flags.Refs[1], err)
		}
	}
	report, err := sem.AnalyzeMergeWithOptions(ctx, repo, base, flags.Refs[0], flags.Refs[1], flags.Depth, sem.MergeOptions{IncludeDocs: flags.IncludeDocs})
	if err != nil {
		return err
	}
	if !flags.NoIntent {
		if err := sem.AttachIntent(ctx, repo, &report); err != nil {
			return err
		}
	}
	if flags.Verify {
		verification, err := sem.VerifyMerge(ctx, repo, flags.Refs[0], flags.Refs[1])
		if err != nil {
			return err
		}
		report.Verification = &verification
	}
	if flags.JSON {
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	writeMergeRadarText(opts.Stdout, report, flags.Depth)
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func writeMergeRadarText(out io.Writer, report sem.MergeReport, depth int) {
	hoisted := hoistedIntents(report)
	fmt.Fprintf(out, "MergeRadar  base %s  depth %d\n", shortSHA(report.Base), depth)
	writeMergeSide(out, "A", report.A, hoisted[report.A.Ref])
	writeMergeSide(out, "B", report.B, hoisted[report.B.Ref])
	writeMergeCoverage(out, report.Analysis)
	fmt.Fprintln(out)

	partial := report.Analysis.Status == sem.AnalysisPartial
	if len(report.Conflicts) == 0 {
		if partial {
			fmt.Fprintln(out, "No conflicts found, but analysis is partial:")
			for _, reason := range report.Analysis.Reasons {
				fmt.Fprintf(out, "  - %s\n", reason)
			}
			fmt.Fprintln(out, "An empty result from a partial analysis is not evidence of safety: the reference the graph could not see may be the one that breaks.")
		} else {
			fmt.Fprintln(out, "No cross-branch conflicts found at this depth.")
			fmt.Fprintln(out, "The graph saw no entity changed on one branch that changed or added code on the other references.")
		}
		fmt.Fprintln(out, "This is not proof the merge is safe: relationships through configuration, serialised data or runtime strings are invisible to the graph.")
	} else {
		structural, heuristic := 0, 0
		for _, c := range report.Conflicts {
			if c.Evidence == sem.EvidenceHeuristic {
				heuristic++
			} else {
				structural++
			}
		}
		fmt.Fprintf(out, "%d potential conflict(s): %d on structural evidence, %d on heuristic evidence. All potential until a merged tree is actually tested.\n\n", len(report.Conflicts), structural, heuristic)
		for i, c := range report.Conflicts {
			writeMergeConflict(out, i+1, c, hoisted)
		}
	}

	writeMergeVerification(out, report)

	if len(report.Warnings) > 0 {
		fmt.Fprintf(out, "\n%d analysis warning(s):\n", len(report.Warnings))
		for _, w := range report.Warnings {
			file := ""
			if w.FilePath != "" {
				file = " " + w.FilePath
			}
			fmt.Fprintf(out, "  %s%s: %s\n", w.Code, file, w.EffectOnCompleteness)
		}
	}
}

// writeMergeVerification prints what running the tests on the merged tree
// showed, or the manual path when nothing was run. Every branch of it names
// the evidence class the reader is left with.
func writeMergeVerification(out io.Writer, report sem.MergeReport) {
	v := report.Verification
	if v == nil {
		fmt.Fprintln(out, "Verify before trusting this report (or rerun with --verify):")
		fmt.Fprintf(out, "  git merge-tree --write-tree %s %s    # scratch merge, no checkout\n", report.A.Ref, report.B.Ref)
		fmt.Fprintln(out, "  then run the tests that touch the files listed above against that tree.")
		return
	}
	switch v.Status {
	case sem.VerificationReproduced:
		fmt.Fprintf(out, "verification REPRODUCED: %s failed on the merged tree %s\n", v.Runner, shortSHA(v.Tree))
		fmt.Fprintf(out, "  %s\n", v.Note)
	case sem.VerificationPassed:
		fmt.Fprintf(out, "verification passed: %s succeeded on the merged tree %s\n", v.Runner, shortSHA(v.Tree))
		fmt.Fprintf(out, "  %s\n", v.Note)
	case sem.VerificationConflicted:
		fmt.Fprintf(out, "verification conflicted: %s\n", v.Note)
	default:
		fmt.Fprintf(out, "verification unavailable: %s\n", v.Note)
	}
	for _, line := range v.Output {
		fmt.Fprintf(out, "    %s\n", line)
	}
	if v.Status != sem.VerificationReproduced && len(v.Manual) > 0 {
		fmt.Fprintln(out, "  Manual path:")
		for _, step := range v.Manual {
			fmt.Fprintf(out, "    %s\n", step)
		}
	}
}

// writeMergeCoverage prints the one line that says how much of the two
// branches the analysis could see, followed by the reasons when it is partial.
func writeMergeCoverage(out io.Writer, analysis sem.MergeAnalysis) {
	if analysis.Status != sem.AnalysisPartial {
		fmt.Fprintln(out, "  analysis complete: every changed file parsed, every matched name resolved to one definition")
		return
	}
	summary := analysis.Summary
	if summary == "" {
		summary = fmt.Sprintf("%d reason(s) the graph may have missed a relationship", len(analysis.Reasons))
	}
	fmt.Fprintf(out, "  analysis PARTIAL: %s\n", summary)
	for _, reason := range analysis.Reasons {
		fmt.Fprintf(out, "    - %s\n", reason)
	}
}

func writeMergeSide(out io.Writer, letter string, side sem.MergeSide, intent *sem.CheckpointIntent) {
	fmt.Fprintf(out, "  %s  %-24s %s  %d files, %d entities changed\n", letter, side.Ref, shortSHA(side.Commit), side.Files, side.Entities)
	if intent != nil {
		writeIntentLine(out, "     ", "intent", intent)
	}
}

// hoistedIntents returns, per branch ref, the single checkpoint intent that
// every conflict on that side quotes, so the text report states it once under
// the branch instead of repeating it under each finding. A side whose conflicts
// quote different checkpoints, or none, gets no entry and keeps its per-conflict
// lines. With no conflicts at all, each side's most recent intent is shown so
// the reader still sees what the two branches set out to do.
func hoistedIntents(report sem.MergeReport) map[string]*sem.CheckpointIntent {
	hoisted := map[string]*sem.CheckpointIntent{}
	if len(report.Conflicts) == 0 {
		for _, side := range []*sem.MergeSide{&report.A, &report.B} {
			if len(side.Intents) > 0 {
				hoisted[side.Ref] = &side.Intents[0]
			}
		}
		return hoisted
	}
	mixed := map[string]bool{}
	consider := func(ref string, intent *sem.CheckpointIntent) {
		if mixed[ref] {
			return
		}
		prev, seen := hoisted[ref]
		switch {
		case intent == nil:
			mixed[ref] = true
			delete(hoisted, ref)
		case !seen:
			hoisted[ref] = intent
		case prev.CheckpointID != intent.CheckpointID || prev.Commit != intent.Commit:
			mixed[ref] = true
			delete(hoisted, ref)
		}
	}
	for _, c := range report.Conflicts {
		consider(c.ChangedOn, c.ChangedIntent)
		if c.Consumer != nil {
			consider(c.ConsumerOn, c.ConsumerIntent)
		}
	}
	return hoisted
}

func writeMergeConflict(out io.Writer, n int, c sem.MergeConflict, hoisted map[string]*sem.CheckpointIntent) {
	evidence := c.Evidence
	if evidence == "" {
		evidence = sem.EvidenceHeuristic
	}
	fmt.Fprintf(out, "[%d] %s  %s evidence · %s until verified\n", n, strings.ToUpper(c.Kind), evidence, c.Confidence)
	for _, note := range c.EvidenceNotes {
		fmt.Fprintf(out, "    evidence    %s\n", note)
	}
	fmt.Fprintf(out, "    changed on %-12s %s %s  %s:%d  %s\n", c.ChangedOn, c.Changed.Kind, c.Changed.Name, c.Changed.Path, c.Changed.Line, c.Changed.Type)
	if c.Changed.OldSignature != "" && c.Changed.NewSignature != "" && c.Changed.OldSignature != c.Changed.NewSignature {
		fmt.Fprintf(out, "      - %s\n      + %s\n", c.Changed.OldSignature, c.Changed.NewSignature)
	}
	if c.Consumer != nil {
		via := ""
		if len(c.Via) > 0 {
			names := make([]string, 0, len(c.Via))
			for _, v := range c.Via {
				names = append(names, v.Name)
			}
			via = "  via " + strings.Join(names, " -> ")
		}
		label := "consumer on"
		if c.Kind == sem.ConflictDirect {
			label = "also changed on"
		}
		fmt.Fprintf(out, "    %-11s %-12s %s %s  %s:%d  %s%s\n", label, c.ConsumerOn, c.Consumer.Kind, c.Consumer.Name, c.Consumer.Path, c.Consumer.Line, c.Consumer.Type, via)
	}
	if hoisted[c.ChangedOn] == nil {
		writeIntentLine(out, "    ", "why "+c.ChangedOn, c.ChangedIntent)
	}
	if c.Consumer != nil && hoisted[c.ConsumerOn] == nil {
		writeIntentLine(out, "    ", "why "+c.ConsumerOn, c.ConsumerIntent)
	}
	fmt.Fprintf(out, "    => %s\n\n", c.Explanation)
}

// intentWrapWidth keeps quoted intents readable in a terminal; the JSON output
// carries them unwrapped.
const intentWrapWidth = 88

func writeIntentLine(out io.Writer, indent, label string, intent *sem.CheckpointIntent) {
	w := max(len(label), 6)
	if intent == nil {
		fmt.Fprintf(out, "%s%-*s (no commits on this side carry checkpoints)\n", indent, w, label)
		return
	}
	if intent.Source == sem.IntentSourceMissing {
		note := intent.Note
		if note == "" {
			note = "no recorded intent"
		}
		fmt.Fprintf(out, "%s%-*s [no intent: %s]  commit %s\n", indent, w, label, note, shortSHA(intent.Commit))
		return
	}
	lines := wrapWords(intent.Intent, intentWrapWidth)
	fmt.Fprintf(out, "%s%-*s \"%s", indent, w, label, lines[0])
	for _, l := range lines[1:] {
		fmt.Fprintf(out, "\n%s%-*s  %s", indent, w, "", l)
	}
	fmt.Fprintln(out, "\"")
	evidence := []string{"checkpoint " + intent.CheckpointID, "commit " + shortSHA(intent.Commit), "from " + intent.Source}
	if intent.Agent != "" {
		evidence = append(evidence, intent.Agent)
	}
	if intent.Match != "" {
		evidence = append(evidence, "matched by "+intent.Match)
	}
	fmt.Fprintf(out, "%s%-*s %s\n", indent, w, "", strings.Join(evidence, " · "))
	if intent.Note != "" {
		fmt.Fprintf(out, "%s%-*s note: %s\n", indent, w, "", intent.Note)
	}
}

// wrapWords splits s into lines of at most width runes at word boundaries. A
// single word longer than width stays on its own line. It always returns at
// least one line so callers can print lines[0] unconditionally.
func wrapWords(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{s}
	}
	var lines []string
	cur := words[0]
	for _, word := range words[1:] {
		if len([]rune(cur))+1+len([]rune(word)) > width {
			lines = append(lines, cur)
			cur = word
			continue
		}
		cur += " " + word
	}
	return append(lines, cur)
}
