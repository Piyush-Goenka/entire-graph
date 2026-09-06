package sem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Verification outcomes. Reproduced and passed are observations from running
// the repository's own tests on a scratch merge; unavailable and conflicted
// mean nothing was observed and the manual path is the only evidence.
const (
	VerificationReproduced  = "reproduced"
	VerificationPassed      = "passed"
	VerificationUnavailable = "unavailable"
	VerificationConflicted  = "conflicted"
)

// MergeVerification records what running tests on the merged tree showed.
// Output keeps the last lines of the runner so the reader sees the failure,
// not a summary of it. Manual always carries the commands to repeat or
// replace the run by hand.
type MergeVerification struct {
	Status string   `json:"status"`
	Tree   string   `json:"tree,omitempty"`
	Runner string   `json:"runner,omitempty"`
	Output []string `json:"output,omitempty"`
	Note   string   `json:"note"`
	Manual []string `json:"manual_steps,omitempty"`
}

// verifyOutputLines bounds how much runner output the report keeps.
const verifyOutputLines = 25

// verifyTimeout bounds one test run on the scratch merge.
const verifyTimeout = 10 * time.Minute

// VerifyMerge builds the merged tree of refA and refB without touching the
// working tree (git merge-tree --write-tree), checks it out in a temporary
// worktree, and runs the repository's tests there. It turns "the graph says
// these might break each other" into an observation. Only a Go module is
// recognised as a test runner today; anything else is reported unavailable
// with the manual commands, never as safe.
func VerifyMerge(ctx context.Context, repo, refA, refB string) (MergeVerification, error) {
	manual := manualVerification(refA, refB)
	out, code, err := gitRun(ctx, repo, "merge-tree", "--write-tree", "--end-of-options", refA, refB)
	if err != nil {
		return MergeVerification{}, err
	}
	tree := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if code == 1 {
		return MergeVerification{
			Status: VerificationConflicted,
			Tree:   tree,
			Note:   "git itself cannot merge these branches cleanly; resolve the textual conflict first, then verify the merged result",
			Output: lastLines(out, verifyOutputLines),
			Manual: manual,
		}, nil
	}
	if code != 0 || tree == "" {
		return MergeVerification{}, fmt.Errorf("git merge-tree --write-tree %s %s: exit %d: %s", refA, refB, code, strings.TrimSpace(out))
	}

	commitOut, code, err := gitRun(ctx, repo, "commit-tree", tree, "-p", refA, "-p", refB, "-m", "merge-radar scratch merge of "+refA+" and "+refB)
	if err != nil {
		return MergeVerification{}, err
	}
	if code != 0 {
		return MergeVerification{
			Status: VerificationUnavailable,
			Tree:   tree,
			Note:   "could not create the scratch merge commit: " + strings.TrimSpace(commitOut),
			Manual: manual,
		}, nil
	}
	commit := strings.TrimSpace(commitOut)

	dir, err := os.MkdirTemp("", "merge-radar-verify-")
	if err != nil {
		return MergeVerification{}, err
	}
	defer func() {
		_, _, _ = gitRun(context.Background(), repo, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
	}()
	if wtOut, code, err := gitRun(ctx, repo, "worktree", "add", "--detach", dir, commit); err != nil {
		return MergeVerification{}, err
	} else if code != 0 {
		return MergeVerification{
			Status: VerificationUnavailable,
			Tree:   tree,
			Note:   "could not check out the scratch merge: " + strings.TrimSpace(wtOut),
			Manual: manual,
		}, nil
	}

	runner, args, ok := detectTestRunner(dir)
	if !ok {
		return MergeVerification{
			Status: VerificationUnavailable,
			Tree:   tree,
			Note:   "no supported test runner found in the merged tree (only Go modules are run automatically); run the repository's tests on the scratch merge by hand",
			Manual: manual,
		}, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, args[0], args[1:]...)
	cmd.Dir = dir
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()
	output := lastLines(combined.String(), verifyOutputLines)
	if runErr == nil {
		return MergeVerification{
			Status: VerificationPassed,
			Tree:   tree,
			Runner: runner,
			Output: output,
			Note:   "the tests passed on the merged tree; the findings stay potential because the suite may not cover the pair of changes",
			Manual: manual,
		}, nil
	}
	var exitErr *exec.ExitError
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return MergeVerification{
			Status: VerificationUnavailable,
			Tree:   tree,
			Runner: runner,
			Output: output,
			Note:   fmt.Sprintf("the test run exceeded %s and was stopped; run it by hand", verifyTimeout),
			Manual: manual,
		}, nil
	}
	if !errors.As(runErr, &exitErr) {
		return MergeVerification{
			Status: VerificationUnavailable,
			Tree:   tree,
			Runner: runner,
			Note:   "could not start the test runner: " + runErr.Error(),
			Manual: manual,
		}, nil
	}
	return MergeVerification{
		Status: VerificationReproduced,
		Tree:   tree,
		Runner: runner,
		Output: output,
		Note:   "the tests fail on the merged tree although each branch passes on its own",
		Manual: manual,
	}, nil
}

// detectTestRunner picks the test command for a checked-out tree.
func detectTestRunner(dir string) (label string, argv []string, ok bool) {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		if _, err := exec.LookPath("go"); err == nil {
			return "go test ./...", []string{"go", "test", "./..."}, true
		}
	}
	return "", nil, false
}

// manualVerification lists the commands a reader can run to build and test
// the merged tree themselves, whatever the language.
func manualVerification(refA, refB string) []string {
	return []string{
		fmt.Sprintf("git merge-tree --write-tree %s %s    # prints the merged tree id; exit 1 means a textual conflict", refA, refB),
		fmt.Sprintf("git commit-tree <tree> -p %s -p %s -m scratch    # wraps it in a dangling commit", refA, refB),
		"git worktree add --detach /tmp/merge-radar <commit>",
		"run the repository's tests inside /tmp/merge-radar",
		"git worktree remove --force /tmp/merge-radar",
	}
}

// gitRun runs one git command in repo and returns its combined output and
// exit code. Exit codes are returned, not turned into errors, because
// merge-tree uses exit 1 to mean "conflicted", which is an answer.
func gitRun(ctx context.Context, repo string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	if err == nil {
		return combined.String(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return combined.String(), exitErr.ExitCode(), nil
	}
	return "", -1, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func lastLines(s string, n int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
