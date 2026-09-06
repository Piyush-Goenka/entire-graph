# MergeRadar

## One-sentence summary

Given two branches built by different agents, MergeRadar combines each branch's
Checkpoint intent with Entire Graph's dependency structure to find changes that
merge cleanly in Git but break each other's assumptions, and shows the evidence.

## Track

Track 2 — Build with Graph Intelligence. The track's own bullet list includes
"combining Entire Graph findings with checkpoint intent," which is this project.

## Problem, intended user and why it matters

Teams now run several coding agents in parallel, each in its own branch or
worktree. File isolation stops textual conflicts. It does nothing about logical
ones. Agent A changes what a function returns; agent B, working at the same
time, writes new code that calls that function assuming the old behaviour. Each
branch passes its own tests. Git merges them without a conflict marker. The
combined program is wrong, and nobody finds out until it runs.

The user is any developer merging agent-built branches, which is now the normal
case.

Semantic merge tools exist. None of them can say *why* each side made its
change, because that reasoning was never recorded before Checkpoints. MergeRadar
explains the conflict in the two agents' own recorded intent, and verifies it
with evidence rather than asserting it.

## Why Entire is essential

Graph is how the tool connects a symbol changed on branch A to the code on
branch B that consumes it. Checkpoints are how it explains the collision: what
each agent was told to do and what it believed. Remove Graph and it is a text
diff. Remove Checkpoints and it is a dependency warning with no explanation,
which existing tools already produce.

## Architecture and main workflow

A new subcommand in the Graph plugin, alongside `diff`, `impact` and
`checkpoint`.

```
entire graph merge-radar <branch-a> <branch-b> [--base <ref>]
```

Steps:

1. Semantic diff of A against base and B against base, using the existing
   `diff` machinery. Output: the set of entities changed, added or removed on
   each side.
2. Cross-reference. For every entity changed on A, walk its consumers with the
   existing neighbours logic and intersect with the set of entities changed or
   added on B. Repeat in the other direction. Each hit is a candidate conflict.
3. Intent. For each entity in a candidate conflict, locate the commit carrying
   its Checkpoint trailer (the plugin's git utilities already do this) and read
   the checkpoint's structured summary to get the recorded intent and outcome
   for that side.
4. Report. One entry per candidate conflict: the symbol, the relationship that
   links the two sides, and the two recorded intents side by side, each with
   the checkpoint id and commit it came from so a human can verify it.
5. Reproduction (afternoon). Create a temporary merge of A and B in a scratch
   worktree and run the tests that Graph says touch the affected entities.
   A failing test upgrades the entry from "potential conflict" to "reproduced".

The guide is explicit that graph results are evidence, not an oracle. Every
entry is labelled `potential` until step 5 reproduces it. The tool never
claims breakage it has not observed.

## Scope

**By 11:45 (pre-noon stable state).** Steps 1 to 4 working end to end on two
real branches in one language. Tests on the cross-reference intersection
(step 2), including the case where the relationship is indirect through an
intermediate caller, and the case where two branches touch the same symbol
with compatible intent. The report prints to the terminal.

**Afternoon.** Step 5, the reproduction. Curveball response. Final semantic
diff of our own change. BUILDATHON.md.

**Explicitly out of scope.** Any UI beyond terminal output. More than one
language. Automatic merge resolution. Live coordination of running sessions.
Named here so we do not drift into them.

## Demonstration

Two agents, two branches, one repository.

Agent A changes the pricing function to return rupees instead of paise. Its
checkpoint intent says so. Agent B, in parallel, builds invoicing that calls
the pricing function assuming paise. Its checkpoint intent says so.

Each branch passes its own tests. `git merge` succeeds with no conflict.

Run MergeRadar. It connects the changed function to its new caller across the
two branches, prints both recorded intents side by side, and, if step 5 is
in, reproduces the wrong invoice.

The pitch line: both agents finished, both branches passed, and here is why
merging them still breaks your app.

## Noon Curveball plan

The engine is a pipeline of four independent steps over two existing data
sources. A new constraint most likely lands on the report's shape, the scope of
what counts as a conflict, or the base-ref handling, rather than on the
intersection logic. Procedure: reconstruct from the pre-noon checkpoint, run
`entire graph impact` on the affected step before editing, implement the
smallest complete response, test it, record a new checkpoint.

## Required checkpoints

1. Initial understanding and intended architecture (this document).
2. Last stable state before noon: steps 1 to 4 and their tests.
3. Response to the Noon Curveball.
4. Final implementation and verification.

## Graph evidence to capture

- Definition lookup while locating the `diff` and `neighbors` entry points.
- Impact analysis before changing how the plugin reads checkpoint data.
- Final semantic diff of the submitted implementation.

Each finding is verified against source and tests and the verification is
recorded, including any case where the graph was wrong or incomplete.

## Known limitations to state honestly

Only one language is supported in the one-day build. The tool detects
relationships that Graph can see; a conflict expressed through configuration,
serialised data or a runtime string will not be found, and the report says so.
Intent is only as good as the checkpoints behind each branch; a branch with no
checkpoint gets a dependency warning with no explanation, clearly labelled.
Reproduction covers only tests Graph associates with the affected entities.

## Next step toward production

Run as a check on pull requests targeting the same base, so the report appears
before a human reviews the merge. Then extend beyond one language.
