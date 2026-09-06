# MergeRadar

## One-sentence summary

`entire graph merge-radar <a> <b>` finds changes on two agent-built branches
that merge cleanly in Git but break each other's assumptions, and explains each
conflict in the two agents' own recorded intent from Entire Checkpoints.

## Problem, intended user and why it matters

Teams now run several coding agents in parallel, each on its own branch or
worktree. File isolation stops textual conflicts. It does nothing about logical
ones. Agent A changes what a function means; agent B, at the same time, writes
new code that calls that function assuming the old meaning. Each branch passes
its own tests. Git merges them without a conflict marker. The combined program
is wrong, and nobody finds out until it runs.

The user is any developer merging agent-built branches, which is now the
normal case. The cost is real: the merged code was never executed by anyone,
and the failure surfaces after review, when both authors have moved on and
neither remembers the assumption that broke.

Semantic merge tools can flag the dependency. None of them can say *why* each
side made its change, because that reasoning was never recorded before
Checkpoints existed.

## Selected Entire track and why Entire is essential

Track 2, Build with Graph Intelligence. The track brief lists "combining Entire
Graph findings with checkpoint intent" as a direction; this is that.

Graph is how the tool connects an entity changed on branch A to code on branch
B that consumes it, including through untouched intermediate callers. Remove
Graph and the tool is a text diff. Checkpoints are how it explains the
collision: what each agent was asked to do and what it believed, quoted from
the checkpoint's AI summary or, when no summary exists, from the session's own
prompt. Remove Checkpoints and it is a dependency warning with no explanation,
which existing tools already give.

The two data sources had never been joined. Graph's `checkpoint` command uses
a checkpoint ID only to locate a commit; nothing in the plugin opened the
transcript or the prompt before this change.

## Architecture and main workflow

One new subcommand in the Graph plugin, built from pieces that already existed.

1. Semantic diff of A against the base and B against the base, using the
   existing `AnalyzeGitRange`. Output: entities changed, added or removed on
   each side.
2. Cross-reference. For every entity changed on A, find entities in B's tree
   whose bodies reference it, using the same reference index that powers
   `dependents_count` (exported as `sem.ReferencesTo`). A referencing entity
   that B also changed or added is a **dependency conflict**. Every referencing
   entity is also a candidate for the next hop, so an untouched intermediate
   caller still connects a change to new code two hops away (default depth 2,
   max 3). Repeated in the other direction. An entity changed on both sides in
   the same file is a **direct conflict**.
3. Intent. For each commit in base..branch, read the `Entire-Checkpoint`
   trailer, resolve `refs/entire/checkpoints/*/<id>`, read the session
   `metadata.json` for the AI summary's intent, outcome and open items, and
   fall back to `prompt.txt` when no summary exists. The checkpoint whose
   `files_touched` includes the conflicting file is chosen; otherwise the
   latest with real intent. The report says which.
4. Report, text or `--json`, one entry per conflict: the changed entity, the
   consumer, the hop chain, both intents with checkpoint id, commit, agent and
   source, and a plain-language explanation. Every entry is labelled
   `potential` and the report ends with the `git merge-tree --write-tree`
   command that produces a merged tree to actually test.

Files: `internal/sem/mergeradar.go` (analysis), `internal/sem/mergeradar_intent.go`
(checkpoint reading), `internal/gitutil/checkpoint_refs.go` and `merge_base.go`
(git plumbing), `internal/cli/mergeradar.go` (command and output), plus one
dispatch line in `root.go` and one help entry in `help.go`.

Honesty rule carried through the code: the tool never claims breakage it has
not observed. A commit without a checkpoint is reported as such, a prompt
quoted in place of a summary is labelled, and a truncated quote is marked.

## Entire Graph findings and verification

- Definition lookup: `entire graph def --repo . --symbol ProviderWarning`
  (afternoon, before the curveball edit) returned the struct at
  internal/sem/provider.go:278 with Code, Severity, FilePath,
  EffectOnCompleteness and Detail. Verified against source: exact. The
  coverage block of the revised report is derived from these typed codes,
  so no second notion of "could not parse" was invented.
- Impact analysis before a risky change: `entire graph impact --repo .
  --symbol ReferencesTo|buildReferenceIndex|dependencyConflicts|directConflicts`
  before the curveball edit. Reported: ReferencesTo is called by
  dependencyConflicts and AnalyzeMerge and wraps buildReferenceIndex, which
  has 11 direct callers (ten dependents tests plus ReferencesTo);
  dependencyConflicts and directConflicts are reached by runMergeRadar and
  five tests and both return MergeConflict. Verified against source: the
  chain is exact. Where the graph was incomplete: `--symbol
  ConfidencePotential` found nothing (a string constant is not indexed);
  the text writer that prints the label (writeMergeConflict) and the "no
  conflicts" sentence never appeared as consumers because the graph tracks
  call edges, not struct-field reads; and every query printed
  "Completeness: degraded" for the graph of this very repository. Both
  gaps were consumers the revision had to touch, so the graph's impact
  list alone would have been short by two sites.
- Final semantic diff: `entire graph diff --repo . --base main --head HEAD`
  on the submitted implementation (evidence/final-semantic-diff.txt in the
  buildathon folder). It listed 15 files and 205 entity changes: every
  function and field added in internal/sem/mergeradar.go (74),
  mergeradar_intent.go (25), mergeradar_verify.go (12),
  internal/cli/mergeradar.go (20), gitutil/checkpoint_refs.go (13) and the
  tests, plus the one-line dispatch change in cli/root.go ("Run body
  changed, 564 dependents"). Verified against `git diff --stat main..HEAD`:
  the file set matches. Where it was incomplete: help.go appears only as
  "module body changed" because the registry is one composite literal, so
  the new help entry and its flags are invisible at entity level; DESIGN.md
  and .claude/settings.json show as document sections (inventory, not
  semantics); and the run carried five E_PARSE_ERROR warnings on vendored
  tree-sitter array.h headers, the same partial coverage the curveball
  response now reports for its own analysis.

## Noon Curveball: what changed and how we adapted

Constraint received at noon (Track 2, "Graph is evidence, not an oracle"):
the product must not present incomplete Graph relationships as certain,
must identify when analysis may be partial, must provide a safe fallback
or verification path, must keep working for fully resolved code, must
include a test or fixture for incomplete analysis, and must let users and
agents tell apart structural evidence, heuristic or incomplete evidence,
and claims that require source or test verification.

**What assumption changed.** Checkpoint 1 decided to reuse the dependents
reference index as the answer to "who references this name". That treated
every index hit as one kind of evidence, gave every finding the same
label, and printed "No cross-branch conflicts found" whenever the index
was empty. The card breaks all three: the index matches by short name, so
a hit can be a different entity that happens to share a name; interface
dispatch, reflection, generated code, prose files and files the parser
cannot handle are invisible; and an empty result from a partial analysis
is not evidence of safety. We proved this on a real repository before
editing: numpy main with two open pull requests (pr-32497 and pr-32510,
curveball-fixture/). The pre-curveball build gave 144, 158, 146 and 146
findings across four runs of the same command, 60 of them hops through
.rst documents by words like "array" and "less", 40 with one end in a C
file that failed to parse, and 772 warnings printed after every finding.
The fresh session reconstructed the work from checkpoints b1b09589d199
and 17790384bf09, then ran `entire graph impact` on ReferencesTo,
buildReferenceIndex, dependencyConflicts and directConflicts before any
edit (see the findings above for where the graph was right and where it
was short).

**How the design changed.** The pipeline, the intent reader and the report
shape are unchanged; four additive things were built, tests first. (1)
Every finding carries an evidence class: structural when each hop
resolved to exactly one parsed entity, heuristic when the matched short
name has several definitions at the consumer head, a hop is a method
(possible dynamic dispatch) or a document, or a provider warning touches
a file in the chain, with a note saying which. Confidence stays
"potential" for both. (2) The report opens with its coverage: complete,
or partial with a one-line summary and the reasons, derived from the
graph's own typed warnings (E_PARSE_ERROR, W_UNSUPPORTED_FILE and the
rest, counted by code and by changed files affected), ambiguous names,
"Code generated" headers and reflect imports. When nothing is found under
partial coverage the text says "No conflicts found, but analysis is
partial:" and never the plain sentence. (3) `--verify` builds the merged
tree with `git merge-tree --write-tree`, checks it out in a temporary
worktree and runs the repository's tests (Go modules today), recording
reproduced with the failing output, passed (still potential), conflicted
when git itself cannot merge, or unavailable with the manual commands.
(4) The walk visits names in sorted order so the count is deterministic,
document entities are excluded unless `--include-docs`, and identical
files on both branches are not reported as direct conflicts. Tests:
TestAnalyzeMergeMarksAmbiguousNameHeuristic,
TestAnalyzeMergeReportsPartialAnalysis,
TestAnalyzeMergeMarksUnparsedFileHeuristic,
TestAnalyzeMergeIsDeterministicWhenTwoOriginsShareAHop,
TestAnalyzeMergeSkipsDocumentsUnlessIncluded,
TestAnalyzeMergeIgnoresIdenticalChangesOnBothSides,
TestVerifyMergeReproducesTheBreak, TestVerifyMergePassesWhenTestsStillPass,
TestVerifyMergeUnavailableWithoutRunner and
TestWriteMergeRadarTextStatesEvidenceAndCoverage; the existing dependency
test now also asserts its finding is structural, which is the proof that
fully resolved code behaves as before.

**Why the new result is safe.** The same numpy command now opens with
"analysis PARTIAL: 229 file(s) failed to parse, 5 unsupported, 18
ambiguous name(s); 5 changed file(s) affected" and names the PRs' own C
files that did not parse, before any finding is read; the count is the
same on every run (57: 2 structural, 55 heuristic; the 20 direct "conflicts"
the old build reported on files pr-32510 shares byte for byte with pr-32497
are gone), every heuristic
finding says why ("ambiguous name: 3 definitions of dispatcher",
"file did not parse: abstractdtypes.c"), and no finding passes through a
document. `--verify` on that pair reports "verification conflicted": git
itself cannot merge the two PRs (add/add on two release-note files), a
fact the old report never mentioned, and prints the manual path. On the
demo fixture the report stays "analysis complete" with two structural
findings and `--verify` reproduces the break: "expected 2.00 rupees, got
0.02". A reader can now tell apart what the graph proved, what it guessed,
and what only a test run can settle, and the tool never says "safe" when
it did not look. BEFORE and AFTER outputs are saved side by side in
curveball-fixture/.

## Checkpoint links and what each checkpoint proves

Fork: https://github.com/Piyush-Goenka/entire-graph (branch merge-radar).
Open any checkpoint with `entire checkpoint explain <id>` in the clone; each
carries an AI summary and the session transcript behind the commit.

1. Initial understanding and intended architecture: checkpoint ea7a792286b0,
   commit 95b4c08 (https://github.com/Piyush-Goenka/entire-graph/commit/95b4c08).
   Proves the design, the rejected alternatives and the assumption ("reuse
   the dependents reference index") that the curveball later invalidated.
2. Last stable state before noon: checkpoint 17790384bf09, commit 6ffa8ef
   (https://github.com/Piyush-Goenka/entire-graph/commit/6ffa8ef). Proves
   steps 1 to 4 working end to end with tests, and names the open risk
   (reproduction printed, not run). Commits 909ab25, c65cacb, 1ff4ba1 and
   7999626 carry the intermediate checkpoints.
3. Response to the Noon Curveball: checkpoint 431773deb4dc, commit 1390a5e
   (https://github.com/Piyush-Goenka/entire-graph/commit/1390a5e). Built
   from the fresh session's transcript, so it contains the reconstruction
   from checkpoints, the impact analysis run before editing, the tests
   written first, and the numpy BEFORE and AFTER runs.
4. Final implementation and verification: the tip of merge-radar
   (https://github.com/Piyush-Goenka/entire-graph/commits/merge-radar), the
   SHA given in the submission. Adds this document and demo/ to the fork and
   records the final semantic diff.

## Setup, run and test instructions

```
git clone <fork>
cd entire-graph
go build -o entire-graph ./cmd/entire-graph          # Go 1.26 (toolchain auto-downloads)
go test ./internal/sem -run 'AnalyzeMerge|CheckpointIntents|TruncateRunes|Verify|DescribeRefs'
go test ./internal/cli -run 'MergeRadar|Registry|EveryCommandDoc'
go test ./internal/gitutil
```

Demo repository (two branches, two agents, checkpoints):

```
demo/make-fixture.sh /tmp/radar-demo --fake-checkpoints   # offline fixture, labelled synthetic
./entire-graph merge-radar pricing-rupees invoicing --repo /tmp/radar-demo
./entire-graph merge-radar pricing-rupees invoicing --repo /tmp/radar-demo --json
./entire-graph merge-radar pricing-rupees invoicing --repo /tmp/radar-demo --verify   # runs the merged tree's tests
demo/make-fixture.sh /tmp/radar-demo --prove              # both branches pass alone; merged tree fails
```

Live demo with two real agents (Entire installed and logged in, Claude Code
on PATH): builds a ten-file Go billing app, records both agent sessions as
checkpoints, generates their AI summaries, then reveals the break.

```
demo/run-agents.sh /tmp/stall prepare units    # or gst, format, random; prints the two agent commands
demo/run-agents.sh /tmp/stall auto             # or paste the two commands into two terminals
demo/run-agents.sh /tmp/stall show             # branches green, merge clean, merged tests fail, MergeRadar explains
```

Recorded reveals for each scenario are in demo/fallback-output-*.txt.

Curveball fixture (numpy, two real pull requests; each run takes about two minutes):

```
cd curveball-fixture/numpy
entire graph merge-radar pr-32497 pr-32510 --repo .            # AFTER output: curveball-fixture/after-merge-radar.txt
entire graph merge-radar pr-32497 pr-32510 --repo . --verify   # git cannot merge the pair: "verification conflicted"
```

As an installed plugin the same command is `entire graph merge-radar ...`.

## Databricks use, data sources and limitations

Not entered.

## Known limitations and next steps

One language exercised end to end (Go); the reference index is language-agnostic
in principle but only Go was tested today. Conflicts expressed through
configuration, serialised data or runtime strings are invisible to the graph and
the report says so. Intent quality depends on checkpoints existing on the
branch; a branch without them gets a dependency warning with no explanation,
clearly labelled. Depth is bounded. `--verify` runs the merged tree's tests only
for Go modules; other ecosystems get "verification unavailable" and the manual
commands, never an implied pass. The evidence class is a conservative rule
(ambiguous name, method, document or unparsed file in the chain), so a real
break can be labelled heuristic; heuristic never means ignored, only "the graph
could not prove this, run the tests". The coverage block reports what the parser
and the reference walk could not see; it cannot report what it never knew to
look for, such as a call built from a string at runtime.

Next step toward production: run as a check on pull requests targeting the same
base so the report appears before a human reviews the merge, add test runners
for the other ecosystems (pytest, npm test, cargo test) so `--verify` can
upgrade `potential` to `reproduced` beyond Go.
