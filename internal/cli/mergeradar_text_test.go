package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/entireio/entire-graph/internal/sem"
)

func textReport(sameCheckpoint bool) sem.MergeReport {
	a1 := &sem.CheckpointIntent{CheckpointID: "AAA1", Commit: "a1a1a1a1a1a1", Source: sem.IntentSourceSummary, Intent: "Return rupees from Price"}
	a2 := a1
	if !sameCheckpoint {
		a2 = &sem.CheckpointIntent{CheckpointID: "AAA2", Commit: "a2a2a2a2a2a2", Source: sem.IntentSourceSummary, Intent: "Also rename the doc comment"}
	}
	b := &sem.CheckpointIntent{CheckpointID: "BBB1", Commit: "b1b1b1b1b1b1", Source: sem.IntentSourcePrompt, Intent: "Price returns paise so divide by 100", Note: "quoting the prompt"}
	price := sem.ChangedEntity{Path: "pricing.go", Kind: "function", Name: "Price", Type: "body_changed", Line: 4}
	invoice := &sem.ChangedEntity{Path: "invoice.go", Kind: "function", Name: "Invoice", Type: "added", Line: 9}
	test := &sem.ChangedEntity{Path: "invoice_test.go", Kind: "function", Name: "TestInvoice", Type: "added", Line: 5}
	return sem.MergeReport{
		SchemaVersion: sem.MergeRadarSchemaVersion, Base: "0000000000000000",
		A: sem.MergeSide{Ref: "pricing-rupees", Commit: "a1a1a1a1a1a1", Files: 2, Entities: 2, Intents: []sem.CheckpointIntent{*a1}},
		B: sem.MergeSide{Ref: "invoicing", Commit: "b1b1b1b1b1b1", Files: 2, Entities: 2, Intents: []sem.CheckpointIntent{*b}},
		Conflicts: []sem.MergeConflict{
			{Kind: sem.ConflictDependency, Confidence: sem.ConfidencePotential, Changed: price, ChangedOn: "pricing-rupees", Consumer: invoice, ConsumerOn: "invoicing", Explanation: "one", ChangedIntent: a1, ConsumerIntent: b},
			{Kind: sem.ConflictDependency, Confidence: sem.ConfidencePotential, Changed: price, ChangedOn: "pricing-rupees", Consumer: test, ConsumerOn: "invoicing", Via: []sem.EntityRef{{Path: "invoice.go", Kind: "function", Name: "Invoice"}}, Explanation: "two", ChangedIntent: a2, ConsumerIntent: b},
		},
	}
}

func TestTextReportHoistsSharedIntentsToTheBranchHeader(t *testing.T) {
	var out bytes.Buffer
	writeMergeRadarText(&out, textReport(true), 2)
	got := out.String()
	if strings.Count(got, "Return rupees from Price") != 1 || strings.Count(got, "divide by 100") != 1 {
		t.Fatalf("each shared intent should be printed exactly once:\n%s", got)
	}
	if strings.Contains(got, "why pricing-rupees") || strings.Contains(got, "why invoicing") {
		t.Fatalf("no per-conflict why lines expected when intents are shared:\n%s", got)
	}
	header := got[:strings.Index(got, "2 potential conflict(s)")]
	if !strings.Contains(header, "intent") || !strings.Contains(header, "checkpoint AAA1") || !strings.Contains(header, "checkpoint BBB1") {
		t.Fatalf("intents should sit under the branch lines:\n%s", header)
	}
	if !strings.Contains(got, "via Invoice") {
		t.Fatalf("hop chain missing:\n%s", got)
	}
}

func TestTextReportKeepsPerConflictIntentsWhenTheyDiffer(t *testing.T) {
	var out bytes.Buffer
	writeMergeRadarText(&out, textReport(false), 2)
	got := out.String()
	if strings.Count(got, "why pricing-rupees") != 2 {
		t.Fatalf("pricing-rupees quotes two checkpoints, so both conflicts need a why line:\n%s", got)
	}
	if strings.Count(got, "divide by 100") != 1 || strings.Contains(got, "why invoicing") {
		t.Fatalf("invoicing still shares one checkpoint and should stay hoisted:\n%s", got)
	}
}

func TestWrapWords(t *testing.T) {
	lines := wrapWords("aaa bbb ccc dddddddddd ee", 8)
	want := []string{"aaa bbb", "ccc", "dddddddddd", "ee"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", lines, want)
	}
	if got := wrapWords("", 8); len(got) != 1 {
		t.Fatalf("empty input must still yield one line, got %v", got)
	}
}
