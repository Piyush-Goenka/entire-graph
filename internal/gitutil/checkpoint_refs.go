package gitutil

import (
	"context"
	"fmt"
	"strings"
)

// CommitCheckpoints pairs one commit with the checkpoint IDs its message
// carries in Entire-Checkpoint trailers. Most commits carry zero or one.
type CommitCheckpoints struct {
	Commit      string
	Subject     string
	Checkpoints []string
}

// CheckpointTrailersInRange lists the commits reachable from head but not
// base, newest first, together with any Entire-Checkpoint trailer values on
// each. Commits without a trailer are still returned so callers can report
// "no checkpoint" honestly rather than silently dropping the commit.
func CheckpointTrailersInRange(ctx context.Context, repo, base, head string) ([]CommitCheckpoints, error) {
	// %x00 separates fields and %x01 terminates records, so a subject line can
	// hold any printable text. Git joins several trailer values with the
	// requested separator (a comma).
	format := "%H%x00%s%x00%(trailers:key=Entire-Checkpoint,valueonly,separator=%x2C)%x01"
	out, err := run(ctx, repo, "git", "log", "--format="+format, base+".."+head)
	if err != nil {
		return nil, err
	}
	var result []CommitCheckpoints
	for _, record := range strings.Split(out, "\x01") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x00", 3)
		if len(parts) < 2 {
			continue
		}
		entry := CommitCheckpoints{Commit: strings.TrimSpace(parts[0]), Subject: parts[1]}
		if len(parts) == 3 {
			for _, id := range strings.Split(parts[2], ",") {
				id = strings.TrimSpace(id)
				if id != "" {
					entry.Checkpoints = append(entry.Checkpoints, id)
				}
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

// CheckpointRef resolves the per-checkpoint ref for an ID without assuming
// the shard scheme: it matches refs/entire/checkpoints/*/<id> and returns the
// first hit. ok is false when the repository has no such ref, which happens
// when the checkpoint lives only on a remote or the commit predates
// checkpoints.
func CheckpointRef(ctx context.Context, repo, checkpointID string) (ref string, ok bool, err error) {
	if checkpointID == "" || strings.ContainsAny(checkpointID, "/ \t\r\n*?[]\\") {
		return "", false, fmt.Errorf("invalid checkpoint id %q", checkpointID)
	}
	out, err := run(ctx, repo, "git", "for-each-ref", "--format=%(refname)", "refs/entire/checkpoints/*/"+checkpointID)
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, true, nil
		}
	}
	return "", false, nil
}

// CheckpointLocation says where a checkpoint's files live. Entire has two
// stores. The refs store keeps one ref per checkpoint
// (refs/entire/checkpoints/<last two chars>/<id>) whose tree is the checkpoint
// itself, so Dir is empty. The shared entire/checkpoints/v1 branch keeps every
// checkpoint of the repository under <first two chars>/<rest of id>/, so Rev
// is the branch and Dir is that directory.
type CheckpointLocation struct {
	Rev string
	Dir string
}

// Path joins a file inside the checkpoint onto Dir, for ShowFile.
func (l CheckpointLocation) Path(rel string) string {
	if l.Dir == "" {
		return rel
	}
	return l.Dir + "/" + rel
}

// Tree is a tree-ish naming the checkpoint's own directory, for ListFiles.
func (l CheckpointLocation) Tree() string {
	if l.Dir == "" {
		return l.Rev
	}
	return l.Rev + ":" + l.Dir
}

// sharedCheckpointBranches lists the local and remote-tracking copies of the
// shared v1 checkpoint branch, local first.
func sharedCheckpointBranches(ctx context.Context, repo string) ([]string, error) {
	out, err := run(ctx, repo, "git", "for-each-ref", "--format=%(refname)",
		"refs/heads/entire/checkpoints/v1", "refs/remotes/*/entire/checkpoints/v1")
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			refs = append(refs, line)
		}
	}
	return refs, nil
}

// FindCheckpoint locates a checkpoint in either store. A per-checkpoint ref
// wins; otherwise each shared v1 branch is checked for the checkpoint's
// metadata.json. ok is false when neither store has it locally.
func FindCheckpoint(ctx context.Context, repo, checkpointID string) (loc CheckpointLocation, ok bool, err error) {
	ref, ok, err := CheckpointRef(ctx, repo, checkpointID)
	if err != nil {
		return CheckpointLocation{}, false, err
	}
	if ok {
		return CheckpointLocation{Rev: ref}, true, nil
	}
	if len(checkpointID) < 3 {
		return CheckpointLocation{}, false, nil
	}
	dir := checkpointID[:2] + "/" + checkpointID[2:]
	branches, err := sharedCheckpointBranches(ctx, repo)
	if err != nil {
		return CheckpointLocation{}, false, err
	}
	for _, branch := range branches {
		_, found, err := ShowFile(ctx, repo, branch, dir+"/metadata.json")
		if err != nil {
			return CheckpointLocation{}, false, err
		}
		if found {
			return CheckpointLocation{Rev: branch, Dir: dir}, true, nil
		}
	}
	return CheckpointLocation{}, false, nil
}
