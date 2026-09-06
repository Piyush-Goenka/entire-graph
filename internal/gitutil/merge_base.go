package gitutil

import (
	"context"
	"strings"
)

// MergeBase returns the best common ancestor of two revisions.
func MergeBase(ctx context.Context, repo, a, b string) (string, error) {
	out, err := run(ctx, repo, "git", "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
