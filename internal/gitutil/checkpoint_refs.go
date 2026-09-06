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
