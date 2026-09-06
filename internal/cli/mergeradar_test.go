package cli

import (
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func TestParseMergeRadarFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    mergeRadarFlags
		wantErr string
	}{
		{
			name: "two branches with defaults",
			args: []string{"feature/a", "feature/b"},
			want: mergeRadarFlags{Depth: 2, Refs: []string{"feature/a", "feature/b"}},
		},
		{
			name: "all flags",
			args: []string{"--repo", ".", "a", "--base", "main", "b", "--depth", "3", "--json", "--no-intent"},
			want: mergeRadarFlags{Repo: ".", Base: "main", Depth: 3, JSON: true, NoIntent: true, Refs: []string{"a", "b"}},
		},
		{
			name: "curveball flags",
			args: []string{"a", "b", "--verify", "--include-docs"},
			want: mergeRadarFlags{Depth: 2, Verify: true, IncludeDocs: true, Refs: []string{"a", "b"}},
		},
		{name: "one branch", args: []string{"a"}, wantErr: "exactly two branches"},
		{name: "three branches", args: []string{"a", "b", "c"}, wantErr: "exactly two branches"},
		{name: "same branch twice", args: []string{"a", "a"}, wantErr: "two different branches"},
		{name: "unknown flag", args: []string{"a", "b", "--bogus"}, wantErr: "unknown flag --bogus"},
		{name: "depth too high", args: []string{"a", "b", "--depth", "9"}, wantErr: "--depth must be"},
		{name: "depth not a number", args: []string{"a", "b", "--depth", "x"}, wantErr: "--depth must be"},
		{name: "base missing value", args: []string{"a", "b", "--base"}, wantErr: "--base requires a value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMergeRadarFlags(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Repo != tc.want.Repo || got.Base != tc.want.Base || got.Depth != tc.want.Depth || got.JSON != tc.want.JSON || got.NoIntent != tc.want.NoIntent || got.Verify != tc.want.Verify || got.IncludeDocs != tc.want.IncludeDocs {
				t.Fatalf("flags = %+v, want %+v", got, tc.want)
			}
			if strings.Join(got.Refs, ",") != strings.Join(tc.want.Refs, ",") {
				t.Fatalf("refs = %v, want %v", got.Refs, tc.want.Refs)
			}
		})
	}
}

func TestMergeRadarHelpIsRegistered(t *testing.T) {
	doc, ok := findCommandDoc("merge-radar")
	if !ok {
		t.Fatal("merge-radar has no help entry")
	}
	if doc.group != groupAnalyze {
		t.Fatalf("merge-radar should sit in the analyze group, got %v", doc.group)
	}
}

func TestWriteMergeRadarTextStatesEvidenceAndCoverage(t *testing.T) {
	side := func(ref string) sem.MergeSide {
		return sem.MergeSide{Ref: ref, Commit: "0123456789abcdef", Files: 1, Entities: 1}
	}
	consumer := sem.ChangedEntity{Path: "invoice.go", Kind: "function", Name: "Invoice", Type: "added", Line: 7}

	structural := sem.MergeReport{
		Base: "abcdef0123456789", A: side("rupees"), B: side("invoicing"),
		Analysis: sem.MergeAnalysis{Status: sem.AnalysisComplete},
		Conflicts: []sem.MergeConflict{{
			Kind: sem.ConflictDependency, Confidence: sem.ConfidencePotential, Evidence: sem.EvidenceStructural,
			Changed:   sem.ChangedEntity{Path: "pricing.go", Kind: "function", Name: "Price", Type: "modified", Line: 4},
			ChangedOn: "rupees", Consumer: &consumer, ConsumerOn: "invoicing", Explanation: "x",
		}},
	}
	var out strings.Builder
	writeMergeRadarText(&out, structural, 2)
	text := out.String()
	if !strings.Contains(text, "[1] DEPENDENCY  structural evidence · potential until verified") {
		t.Fatalf("structural finding should carry its evidence class, got:\n%s", text)
	}
	if !strings.Contains(text, "analysis complete") {
		t.Fatalf("complete coverage should be stated under the header, got:\n%s", text)
	}

	heuristic := structural
	heuristic.Conflicts = []sem.MergeConflict{structural.Conflicts[0]}
	heuristic.Conflicts[0].Evidence = sem.EvidenceHeuristic
	heuristic.Conflicts[0].EvidenceNotes = []string{"ambiguous name: 2 definitions of Price on invoicing"}
	out.Reset()
	writeMergeRadarText(&out, heuristic, 2)
	text = out.String()
	if !strings.Contains(text, "[1] DEPENDENCY  heuristic evidence · potential until verified") || !strings.Contains(text, "ambiguous name: 2 definitions of Price") {
		t.Fatalf("heuristic finding should carry its class and reason, got:\n%s", text)
	}

	partial := sem.MergeReport{
		Base: "abcdef0123456789", A: side("rupees"), B: side("broken"),
		Analysis: sem.MergeAnalysis{Status: sem.AnalysisPartial, Summary: "767 file(s) failed to parse, 5 unsupported; 3 changed file(s) affected", Reasons: []string{"E_PARSE_ERROR on broken: invoice.go could not be parsed"}},
	}
	out.Reset()
	writeMergeRadarText(&out, partial, 2)
	text = out.String()
	if strings.Contains(text, "No cross-branch conflicts found") {
		t.Fatalf("partial analysis must never print the plain no-conflicts sentence, got:\n%s", text)
	}
	if !strings.Contains(text, "No conflicts found, but analysis is partial:") || !strings.Contains(text, "E_PARSE_ERROR on broken") {
		t.Fatalf("partial analysis should say so and list the reasons, got:\n%s", text)
	}
	header := strings.SplitN(text, "\n\n", 2)[0]
	if !strings.Contains(header, "analysis PARTIAL: 767 file(s) failed to parse, 5 unsupported; 3 changed file(s) affected") {
		t.Fatalf("coverage must be stated in the header block, before any finding, got:\n%s", text)
	}
	if !strings.Contains(text, "git merge-tree --write-tree rupees broken") {
		t.Fatalf("without --verify the manual verification command is printed, got:\n%s", text)
	}

	unavailable := partial
	unavailable.Verification = &sem.MergeVerification{Status: sem.VerificationUnavailable, Note: "no supported test runner found in the merged tree", Manual: []string{"git merge-tree --write-tree rupees broken"}}
	out.Reset()
	writeMergeRadarText(&out, unavailable, 2)
	text = out.String()
	if !strings.Contains(text, "verification unavailable: no supported test runner") || !strings.Contains(text, "git merge-tree --write-tree rupees broken") {
		t.Fatalf("unavailable verification must say so and print the manual path, got:\n%s", text)
	}

	reproduced := structural
	reproduced.Verification = &sem.MergeVerification{Status: sem.VerificationReproduced, Runner: "go test ./...", Tree: "0123456789abcdef", Output: []string{"invoice.go:9: mismatched types", "FAIL"}}
	out.Reset()
	writeMergeRadarText(&out, reproduced, 2)
	text = out.String()
	if !strings.Contains(text, "verification REPRODUCED") || !strings.Contains(text, "mismatched types") {
		t.Fatalf("a reproduced break must be stated with its output, got:\n%s", text)
	}
}
