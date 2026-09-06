#!/usr/bin/env bash
# Build the MergeRadar demo repository.
#
#   make-fixture.sh <dir>                    base repo only; prints the two agent prompts
#   make-fixture.sh <dir> --fake-checkpoints full offline fixture with synthetic checkpoint refs
#   make-fixture.sh <dir> --prove            scratch-merge the two branches and run the tests
#
# Story: base has Price() in paise. Agent A (branch pricing-rupees) changes
# Price() to return rupees with the same signature. Agent B (branch invoicing)
# adds Invoice(), summing Price() and dividing by 100 because it assumes paise.
# Each branch passes its own tests. git merges them cleanly. The merged invoice
# is wrong by 100x.
#
# For the judged demo use the default mode, then let a real coding agent (with
# Entire enabled in <dir>) make each branch's commit from the printed prompt so
# the checkpoints are real. --fake-checkpoints is for dry runs only and says so.
set -euo pipefail

DIR=${1:?usage: make-fixture.sh <dir> [--fake-checkpoints|--prove]}
MODE=${2:-}

if [ "$MODE" = "--prove" ]; then
  cd "$DIR"
  echo "== each branch on its own:"
  for b in pricing-rupees invoicing; do
    git checkout -q "$b"
    printf '  %-16s ' "$b"; go test ./... 2>&1 | tail -1
  done
  git checkout -q main
  echo
  echo "== git merge: "
  tree=$(git merge-tree --write-tree pricing-rupees invoicing) && echo "  clean, no conflict markers (tree $tree)"
  commit=$(git commit-tree "$tree" -p pricing-rupees -p invoicing -m "scratch merge for verification")
  scratch=$(mktemp -d)
  git worktree add -q --detach "$scratch" "$commit"
  echo
  echo "== merged tree, same tests:"
  (cd "$scratch" && go test ./... 2>&1 | grep -E 'FAIL|ok|expected' | sed 's/^/  /') || true
  git worktree remove --force "$scratch"
  exit 0
fi

rm -rf "$DIR"; mkdir -p "$DIR"; cd "$DIR"
git init -q
git checkout -q -b main 2>/dev/null || git branch -M main
printf 'module example.com/shop\n\ngo 1.22\n' > go.mod
cat > pricing.go <<'EOF'
package shop

// Price returns the unit price of an item in paise.
func Price(item string) int {
	return 100
}
EOF
cat > invoice.go <<'EOF'
package shop

func Header() string {
	return "INVOICE"
}
EOF
git add . && git commit -q -m "base: pricing in paise"

PROMPT_A='Change Price() in pricing.go to return the price in rupees instead of paise. Keep the int signature. Update the doc comment and add pricing_test.go asserting Price("chai") == 1. Do not touch any other file.'
PROMPT_B='Add an Invoice(items []string) float64 function to invoice.go that sums Price() for every item and returns the total in rupees. Price() returns paise, so divide by 100. Add invoice_test.go asserting Invoice([]string{"chai","samosa"}) == 2.0. Do not modify pricing.go.'

if [ "$MODE" != "--fake-checkpoints" ]; then
  cat <<EOF

Base repo ready at $DIR (branch main).

Now produce the two branches with a REAL agent so Entire records real checkpoints:

  cd $DIR
  entire enable -y --agent claude && entire status

  git checkout -b pricing-rupees
  # start a fresh agent session and paste:
  #   $PROMPT_A
  # then commit; Entire attaches the checkpoint.

  git checkout main && git checkout -b invoicing
  # start ANOTHER fresh agent session and paste:
  #   $PROMPT_B
  # then commit.

  git checkout main
  entire graph merge-radar pricing-rupees invoicing --repo .
  $(dirname "$0")/make-fixture.sh $DIR --prove     # show both pass alone and the merge breaks
EOF
  exit 0
fi

echo "(fake checkpoints: synthetic refs shaped like the Entire refs backend; for dry runs only)"
fake_checkpoint() { # id summary prompt file
  local id=$1 summary=$2 prompt=$3 file=$4 sjson=""
  [ -n "$summary" ] && sjson=",\"summary\":{\"intent\":\"$summary\",\"outcome\":\"done\",\"open_items\":[]}"
  local root sess pr st rt c
  root=$(printf '{"checkpoint_id":"%s","sessions":[{"metadata":"/1/metadata.json","prompt":"/1/prompt.txt"}]}' "$id" | git hash-object -w --stdin)
  sess=$(printf '{"checkpoint_id":"%s","agent":"Claude Code","model":"claude-opus-5","created_at":"2026-09-06T10:00:00Z","files_touched":["%s"]%s}' "$id" "$file" "$sjson" | git hash-object -w --stdin)
  pr=$(printf '%s' "$prompt" | git hash-object -w --stdin)
  st=$(printf '100644 blob %s\tmetadata.json\n100644 blob %s\tprompt.txt\n' "$sess" "$pr" | git mktree)
  rt=$(printf '100644 blob %s\tmetadata.json\n040000 tree %s\t1\n' "$root" "$st" | git mktree)
  c=$(git commit-tree "$rt" -m "checkpoint $id")
  git update-ref "refs/entire/checkpoints/${id: -2}/$id" "$c"
}

git checkout -q -b pricing-rupees
cat > pricing.go <<'EOF'
package shop

// Price returns the unit price of an item in rupees.
func Price(item string) int {
	return 1
}
EOF
cat > pricing_test.go <<'EOF'
package shop

import "testing"

func TestPriceIsInRupees(t *testing.T) {
	if Price("chai") != 1 {
		t.Fatal("expected 1 rupee")
	}
}
EOF
A_ID=01K4RUPEES00000000000000AA
fake_checkpoint $A_ID "Switch Price to return rupees instead of paise so totals are human readable" "$PROMPT_A" pricing.go
git add . && git commit -q -m "pricing: return rupees" -m "Entire-Checkpoint: $A_ID"

git checkout -q main && git checkout -q -b invoicing
cat > invoice.go <<'EOF'
package shop

func Header() string {
	return "INVOICE"
}

// Invoice totals item prices. Price() is in paise, so divide by 100 for rupees.
func Invoice(items []string) float64 {
	paise := 0
	for _, item := range items {
		paise += Price(item)
	}
	return float64(paise) / 100
}
EOF
cat > invoice_test.go <<'EOF'
package shop

import "testing"

func TestInvoiceConvertsPaiseToRupees(t *testing.T) {
	if got := Invoice([]string{"chai", "samosa"}); got != 2.0 {
		t.Fatalf("expected 2.00 rupees, got %v", got)
	}
}
EOF
B_ID=01K4INVOICE0000000000000BB
fake_checkpoint $B_ID "" "$PROMPT_B" invoice.go
git add . && git commit -q -m "invoice: total items" -m "Entire-Checkpoint: $B_ID"
git checkout -q main

echo "fixture ready at $DIR"
echo "  entire graph merge-radar pricing-rupees invoicing --repo $DIR"
echo "  $(dirname "$0")/make-fixture.sh $DIR --prove"
