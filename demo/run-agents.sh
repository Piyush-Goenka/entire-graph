#!/usr/bin/env bash
# Live two-agent demo for MergeRadar on the chai-stall billing app (demo/make-app.sh).
#
#   run-agents.sh <dir> prepare [units|gst|format|random]
#                                 builds the app, enables Entire, creates two worktrees and
#                                 prints the two agent commands to run side by side
#   run-agents.sh <dir> auto      runs both agents headless in parallel, commits each, and
#                                 generates the AI summary of each checkpoint
#   run-agents.sh <dir> show      the reveal: each branch green alone with its receipt, git
#                                 merge clean, merged tests fail, then MergeRadar explains
#                                 why in each agent's recorded intent
#
# Every scenario has agent A change existing behaviour and agent B add a feature that
# assumes the old behaviour, in disjoint files, so git merges cleanly and only the
# combination is wrong:
#   units    A: Price() and Money() move from paise to rupees      B: loyalty points from Subtotal(), assuming paise
#   gst      A: prices become tax-inclusive, GST() extracts         B: party quotes add GST on top
#   format   A: Money() prints Indian currency with the rupee sign B: CSV export parses Money() back
set -euo pipefail

DIR=${1:?usage: run-agents.sh <dir> prepare [scenario]|auto|show}
MODE=${2:-prepare}
HERE=$(cd "$(dirname "$0")" && pwd)
H=$(dirname "$HERE")
export PATH="$H/bin:$PATH"
NAME=$(basename "$DIR")
WT_A="$(dirname "$DIR")/$NAME-agent-A"
WT_B="$(dirname "$DIR")/$NAME-agent-B"
TOOLS='Read,Edit,Write,Bash(go test:*),Bash(go build:*),Bash(go vet:*),Bash(gofmt:*),Bash(git status:*),Bash(git diff:*),Bash(ls:*),Bash(cat:*)'
ORDER="chai samosa"   # the order whose receipt the reveal prints

scenario() { # sets SCEN BR_A BR_B DESC_A DESC_B MSG_A MSG_B PROMPT_A PROMPT_B
  SCEN=$1
  if [ "$SCEN" = random ]; then SCEN=$(printf 'units\ngst\nformat\n' | awk 'BEGIN{srand()} {a[NR]=$0} END{print a[int(rand()*NR)+1]}'); fi
  case "$SCEN" in
    units)
      BR_A=pricing-rupees; BR_B=loyalty-points
      DESC_A="Price() and Money() move from paise to rupees"; DESC_B="loyalty points computed from Subtotal(), assuming paise"
      MSG_A="pricing: rupees instead of paise"; MSG_B="loyalty: points from the subtotal"
      PROMPT_A='The stall is moving its money to whole rupees. In pricing.go change Price() to return the unit price in rupees instead of paise (the catalog stores paise, so divide by 100; keep the int return type) and update its doc comment. Subtotal() sums Price and needs no change. In format.go change Money() to take an amount in rupees instead of paise and format it with two decimals (25 -> "25.00"). Update the existing tests in pricing_test.go, format_test.go and receipt_test.go so they pass with the new units. Run go test ./... until green. Do not create new files, do not touch catalog.go, tax.go, discount.go or receipt.go, and do not commit.'
      PROMPT_B='Add loyalty points. Create loyalty.go with Points(items []string) int that awards 1 point for every 10 rupees spent, based on Subtotal(items). Subtotal is in paise, so divide by 1000. Create loyalty_test.go asserting Points([]string{"chai", "samosa"}) == 2 and Points([]string{"bun"}) == 0. Run go test ./... until green. Do not modify any existing file and do not commit.'
      ;;
    gst)
      BR_A=tax-inclusive-prices; BR_B=party-quotes
      DESC_A="prices become tax-inclusive MRP; GST() extracts the included tax"; DESC_B="party quotes that add GST on top of the subtotal"
      MSG_A="pricing: tax-inclusive MRP"; MSG_B="quotes: party quote with GST and service charge"
      PROMPT_A='Menu prices must now be tax-inclusive MRP. In pricing.go make Price() return the catalog price plus 5% GST (paise * 105 / 100) and say so in its doc comment. In tax.go change GST(amount) to return the tax already included in an inclusive amount (amount * 5 / 105) instead of adding tax on top, and update its doc comment. In receipt.go make Bill() set Total to Subtotal minus Discount, with Tax reported as the included GST(taxable) but not added again. Update pricing_test.go and receipt_test.go so they pass. Run go test ./... until green. Do not create new files, do not touch format.go, catalog.go or discount.go, and do not commit.'
      PROMPT_B='Add party quotes. Create quote.go with PartyQuote(items []string, guests int) int that returns, in paise, the cost of serving the order to every guest: Subtotal(items) plus GST(Subtotal(items)) as the tax on top, times guests, plus a 10% service charge on the whole. Create quote_test.go asserting PartyQuote([]string{"chai", "samosa"}, 2) == 5775 (2500 + 125 tax = 2625 per guest, times 2 = 5250, plus 10% = 5775). Run go test ./... until green. Do not modify any existing file and do not commit.'
      ;;
    format)
      BR_A=indian-money-format; BR_B=csv-export
      DESC_A="Money() prints the rupee sign with Indian digit grouping"; DESC_B="CSV export that parses Money() output back into numbers"
      MSG_A="format: Indian currency formatting"; MSG_B="export: CSV for the accountant"
      PROMPT_A='Receipts must show Indian currency formatting. In format.go change Money() to prefix the rupee sign and group the rupee digits the Indian way: 1050 -> "₹10.50", 123456 -> "₹1,234.56", 12345678 -> "₹1,23,456.78". Update format_test.go, and receipt_test.go if it checks a formatted amount, so they pass. Run go test ./... until green. Do not create new files, do not touch pricing.go, tax.go, receipt.go or catalog.go, and do not commit.'
      PROMPT_B='Add a CSV export for the accountant. Create export.go with ExportCSV(r Receipt) string that writes one line per receipt line as name,amount and a final line total,amount, using Money() for every amount. Create export_test.go that exports Bill([]string{"chai", "samosa"}, ""), parses the amount column of every line with strconv.ParseFloat, and asserts the parsed total line equals 26.25. Run go test ./... until green. Do not modify any existing file and do not commit.'
      ;;
    *) echo "unknown scenario '$SCEN' (units|gst|format|random)" >&2; exit 1 ;;
  esac
}

sq() { case $1 in *\'*) printf %q "$1";; *) printf "'%s'" "$1";; esac; } # quote for pasting into a shell
agent_cmd() { # worktree prompt [headless]
  if [ "${3:-}" = headless ]; then
    printf 'cd %q && timeout 600 env -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT claude -p %q --allowedTools %q --output-format text' "$1" "$2" "$TOOLS"
  else
    printf 'cd %s && claude --allowedTools %s %s' "$(sq "$1")" "$(sq "$TOOLS")" "$(sq "$2")"
  fi
}
generate_line() { printf "entire checkpoint explain --generate \$(git log -1 --format='%%(trailers:key=Entire-Checkpoint,valueonly)')   # AI summary, ~10s"; }

case "$MODE" in
  prepare)
    scenario "${3:-units}"
    [ -e "$DIR" ] && { echo "$DIR exists; pick a fresh path" >&2; exit 1; }
    "$HERE/make-app.sh" "$DIR" >/dev/null
    cd "$DIR"
    entire enable -y --agent claude-code >/dev/null
    # Entire's agent hooks live in tracked files (.claude/settings.json, .entire/).
    # Commit them on main so both worktrees inherit them and both sessions are recorded.
    git add -A >/dev/null 2>&1 && git commit -q -m "chore: enable Entire checkpoints" >/dev/null 2>&1 || true
    printf '%s\n' "$SCEN" > .git/demo-scenario
    entire status | head -2
    rm -rf "$WT_A" "$WT_B"
    git worktree add -q -b "$BR_A" "$WT_A" main
    git worktree add -q -b "$BR_B" "$WT_B" main
    cat <<EOF

Chai-stall billing app at $DIR ($(git ls-files '*.go' | wc -l | tr -d ' ') Go files), Entire recording, scenario "$SCEN":
  agent A on $BR_A: $DESC_A
  agent B on $BR_B: $DESC_B

Terminal 1 (agent A):
  $(agent_cmd "$WT_A" "$PROMPT_A")
  # when the agent is done, exit it and commit in the same terminal:
  cd $WT_A && git add -A && git commit -m "$MSG_A"
  $(generate_line)

Terminal 2 (agent B):
  $(agent_cmd "$WT_B" "$PROMPT_B")
  cd $WT_B && git add -A && git commit -m "$MSG_B"
  $(generate_line)

Or run both headless:   $0 $DIR auto
Then the reveal:        $0 $DIR show
EOF
    ;;
  auto)
    scenario "$(cat "$DIR/.git/demo-scenario")"
    echo "== scenario $SCEN: two agents in parallel, Entire recording each session"
    echo "   A on $BR_A: $DESC_A"; echo "   B on $BR_B: $DESC_B"
    run_agent() { # letter worktree prompt msg
      local log; log="$(dirname "$DIR")/$NAME-agent-$1.log"
      if eval "$(agent_cmd "$2" "$3" headless)" > "$log" 2>&1; then echo "agent $1 finished"; else echo "agent $1 exited with $? (see $log)"; fi
      (cd "$2" && git add -A && git commit -q -m "$4" && echo "agent $1 committed: $(git log -1 --format='%h %s')")
    }
    run_agent A "$WT_A" "$PROMPT_A" "$MSG_A" &
    run_agent B "$WT_B" "$PROMPT_B" "$MSG_B" &
    wait
    echo; echo "== AI summaries for both checkpoints (entire checkpoint explain --generate)"
    gen() { # letter worktree
      local id; id=$(cd "$2" && git log -1 --format='%(trailers:key=Entire-Checkpoint,valueonly)' | tr -d '[:space:]')
      [ -n "$id" ] || { echo "agent $1: commit carries no checkpoint"; return 0; }
      if (cd "$2" && timeout 150 env -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT entire checkpoint explain --generate "$id" </dev/null >/dev/null 2>&1); then
        echo "agent $1: summary generated for $id"
      else
        echo "agent $1: no summary for $id; MergeRadar will quote the prompt instead"
      fi
    }
    gen A "$WT_A" &
    gen B "$WT_B" &
    wait
    cd "$DIR"
    echo; echo "== branch tips and their checkpoints:"
    git log --branches --no-walk --decorate --format='  %h%d %s%n      Entire-Checkpoint: %(trailers:key=Entire-Checkpoint,valueonly)'
    ;;
  show)
    scenario "$(cat "$DIR/.git/demo-scenario")"
    cd "$DIR"
    echo "== scenario $SCEN"
    for pair in "A:$BR_A:$WT_A:$DESC_A" "B:$BR_B:$WT_B:$DESC_B"; do
      IFS=: read -r L B W D <<<"$pair"
      echo; echo "-- agent $L, branch $B ($D):"
      (cd "$W" && go test ./... 2>&1 | grep -v 'no test files' | tail -1 | sed 's/^/  /'; go run ./cmd/stall $ORDER | sed 's/^/     /')
    done
    echo; echo "== git merge $BR_A + $BR_B:"
    tree=$(git merge-tree --write-tree "$BR_A" "$BR_B") && echo "  clean, no conflict markers"
    commit=$(git commit-tree "$tree" -p "$BR_A" -p "$BR_B" -m "scratch merge for verification")
    scratch=$(mktemp -d); git worktree add -q --detach "$scratch" "$commit"
    echo; echo "== merged tree: receipt, then the same tests"
    (cd "$scratch" && go run ./cmd/stall $ORDER | sed 's/^/     /'; go test ./... 2>&1 | grep -v 'no test files' | grep -v '^[[:space:]]*$' | tail -8 | sed 's/^/  /') || true
    git worktree remove --force "$scratch"
    echo; echo "== entire graph merge-radar $BR_A $BR_B --verify"; echo
    entire graph merge-radar "$BR_A" "$BR_B" --repo . --verify
    ;;
  *) echo "unknown mode $MODE" >&2; exit 1 ;;
esac
