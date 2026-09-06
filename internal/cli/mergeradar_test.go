package cli

import (
	"strings"
	"testing"
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
			if got.Repo != tc.want.Repo || got.Base != tc.want.Base || got.Depth != tc.want.Depth || got.JSON != tc.want.JSON || got.NoIntent != tc.want.NoIntent {
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
