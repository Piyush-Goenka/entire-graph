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
	Repo     string
	Base     string
	JSON     bool
	NoIntent bool
	Depth    int
	Refs     []string
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
	report, err := sem.AnalyzeMerge(ctx, repo, base, flags.Refs[0], flags.Refs[1], flags.Depth)
	if err != nil {
		return err
	}
	if !flags.NoIntent {
		if err := sem.AttachIntent(ctx, repo, &report); err != nil {
			return err
		}
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
	fmt.Fprintf(out, "MergeRadar  base %s  depth %d\n", shortSHA(report.Base), depth)
	fmt.Fprintf(out, "  A  %-24s %s  %d files, %d entities changed\n", report.A.Ref, shortSHA(report.A.Commit), report.A.Files, report.A.Entities)
	fmt.Fprintf(out, "  B  %-24s %s  %d files, %d entities changed\n\n", report.B.Ref, shortSHA(report.B.Commit), report.B.Files, report.B.Entities)

	if len(report.Conflicts) == 0 {
		fmt.Fprintln(out, "No cross-branch conflicts found at this depth.")
		fmt.Fprintln(out, "The graph saw no entity changed on one branch that changed or added code on the other references.")
		fmt.Fprintln(out, "This is not proof the merge is safe: relationships through configuration, serialised data or runtime strings are invisible to the graph.")
	} else {
		fmt.Fprintf(out, "%d potential conflict(s). Labelled potential until a merged tree is actually tested.\n\n", len(report.Conflicts))
		for i, c := range report.Conflicts {
			writeMergeConflict(out, i+1, c)
		}
	}

	fmt.Fprintln(out, "Verify before trusting this report:")
	fmt.Fprintf(out, "  git merge-tree --write-tree %s %s    # scratch merge, no checkout\n", report.A.Ref, report.B.Ref)
	fmt.Fprintln(out, "  then run the tests that touch the files listed above against that tree.")

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

func writeMergeConflict(out io.Writer, n int, c sem.MergeConflict) {
	fmt.Fprintf(out, "[%d] %s  %s\n", n, strings.ToUpper(c.Kind), c.Confidence)
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
	writeIntentLine(out, "why "+c.ChangedOn, c.ChangedIntent)
	if c.Consumer != nil {
		writeIntentLine(out, "why "+c.ConsumerOn, c.ConsumerIntent)
	}
	fmt.Fprintf(out, "    => %s\n\n", c.Explanation)
}

func writeIntentLine(out io.Writer, label string, intent *sem.CheckpointIntent) {
	if intent == nil {
		fmt.Fprintf(out, "    %-24s (no commits on this side carry checkpoints)\n", label)
		return
	}
	if intent.Source == sem.IntentSourceMissing {
		note := intent.Note
		if note == "" {
			note = "no recorded intent"
		}
		fmt.Fprintf(out, "    %-24s [no intent: %s]  commit %s\n", label, note, shortSHA(intent.Commit))
		return
	}
	fmt.Fprintf(out, "    %-24s \"%s\"\n", label, intent.Intent)
	evidence := []string{"checkpoint " + intent.CheckpointID, "commit " + shortSHA(intent.Commit), "from " + intent.Source}
	if intent.Agent != "" {
		evidence = append(evidence, intent.Agent)
	}
	if intent.Match != "" {
		evidence = append(evidence, "matched by "+intent.Match)
	}
	fmt.Fprintf(out, "    %-24s %s\n", "", strings.Join(evidence, " · "))
	if intent.Note != "" {
		fmt.Fprintf(out, "    %-24s note: %s\n", "", intent.Note)
	}
}
