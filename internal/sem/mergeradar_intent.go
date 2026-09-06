package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/entireio/entire-graph/internal/gitutil"
)

// Intent sources, in order of how much they say. A summary is the AI-written
// intent stored on the checkpoint. A prompt is the session's raw user prompt,
// used when no summary exists. Missing means the commit carried no usable
// checkpoint and the Note says why.
const (
	IntentSourceSummary = "summary"
	IntentSourcePrompt  = "prompt"
	IntentSourceMissing = "missing"
)

// maxPromptIntentBytes bounds how much of a raw prompt is quoted.
const maxPromptIntentBytes = 600

// CheckpointIntent is what one checkpoint session records about why its
// commit happened. Every field except Commit and Source may be empty; Note
// explains any degradation so a reader never mistakes absence for silence.
type CheckpointIntent struct {
	CheckpointID string   `json:"checkpoint_id,omitempty"`
	Commit       string   `json:"commit"`
	Agent        string   `json:"agent,omitempty"`
	Model        string   `json:"model,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	Source       string   `json:"source"`
	Intent       string   `json:"intent,omitempty"`
	Outcome      string   `json:"outcome,omitempty"`
	OpenItems    []string `json:"open_items,omitempty"`
	FilesTouched []string `json:"files_touched,omitempty"`
	// Match records how this intent was chosen for a conflict: files_touched
	// when the checkpoint lists the conflicting file, latest otherwise.
	Match string `json:"match,omitempty"`
	Note  string `json:"note,omitempty"`
}

// checkpointSessionMeta mirrors only the fields of the Entire CLI's session
// metadata.json that this analysis reads. Unknown fields are ignored.
type checkpointSessionMeta struct {
	Agent        string   `json:"agent"`
	Model        string   `json:"model"`
	CreatedAt    string   `json:"created_at"`
	FilesTouched []string `json:"files_touched"`
	Summary      *struct {
		Intent    string   `json:"intent"`
		Outcome   string   `json:"outcome"`
		OpenItems []string `json:"open_items"`
	} `json:"summary"`
}

// CheckpointIntents reads the recorded intent behind every commit in
// base..head, newest first. Commits without a checkpoint are reported with
// Source missing rather than dropped.
func CheckpointIntents(ctx context.Context, repo, base, head string) ([]CheckpointIntent, error) {
	commits, err := gitutil.CheckpointTrailersInRange(ctx, repo, base, head)
	if err != nil {
		return nil, fmt.Errorf("list commits %s..%s: %w", base, head, err)
	}
	var out []CheckpointIntent
	for _, commit := range commits {
		if len(commit.Checkpoints) == 0 {
			out = append(out, CheckpointIntent{
				Commit: commit.Commit,
				Source: IntentSourceMissing,
				Note:   "commit has no Entire-Checkpoint trailer",
			})
			continue
		}
		for _, id := range commit.Checkpoints {
			intents, err := readCheckpointIntents(ctx, repo, commit.Commit, id)
			if err != nil {
				return nil, err
			}
			out = append(out, intents...)
		}
	}
	return out, nil
}

func readCheckpointIntents(ctx context.Context, repo, commit, checkpointID string) ([]CheckpointIntent, error) {
	missing := func(note string) []CheckpointIntent {
		return []CheckpointIntent{{CheckpointID: checkpointID, Commit: commit, Source: IntentSourceMissing, Note: note}}
	}
	loc, ok, err := gitutil.FindCheckpoint(ctx, repo, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("resolve checkpoint %s: %w", checkpointID, err)
	}
	if !ok {
		return missing("checkpoint is not present locally; fetch refs/entire/checkpoints/* or the entire/checkpoints/v1 branch from the remote"), nil
	}
	files, err := gitutil.ListFiles(ctx, repo, loc.Tree())
	if err != nil {
		return nil, fmt.Errorf("list checkpoint %s: %w", checkpointID, err)
	}
	var sessionMetas []string
	for _, file := range files {
		if file != "metadata.json" && strings.HasSuffix(file, "/metadata.json") {
			sessionMetas = append(sessionMetas, file)
		}
	}
	sort.Strings(sessionMetas)
	if len(sessionMetas) == 0 {
		return missing("checkpoint has no session metadata"), nil
	}

	var out []CheckpointIntent
	for _, metaPath := range sessionMetas {
		intent := CheckpointIntent{CheckpointID: checkpointID, Commit: commit, Source: IntentSourceMissing}
		content, found, err := gitutil.ShowFile(ctx, repo, loc.Rev, loc.Path(metaPath))
		if err != nil {
			return nil, fmt.Errorf("read %s from checkpoint %s: %w", metaPath, checkpointID, err)
		}
		if !found {
			intent.Note = "session metadata could not be read"
			out = append(out, intent)
			continue
		}
		var meta checkpointSessionMeta
		if err := json.Unmarshal([]byte(content), &meta); err != nil {
			intent.Note = fmt.Sprintf("session metadata is not valid JSON: %v", err)
			out = append(out, intent)
			continue
		}
		intent.Agent, intent.Model, intent.CreatedAt, intent.FilesTouched = meta.Agent, meta.Model, meta.CreatedAt, meta.FilesTouched

		if meta.Summary != nil && strings.TrimSpace(meta.Summary.Intent) != "" {
			intent.Source = IntentSourceSummary
			intent.Intent = strings.TrimSpace(meta.Summary.Intent)
			intent.Outcome = strings.TrimSpace(meta.Summary.Outcome)
			intent.OpenItems = meta.Summary.OpenItems
			out = append(out, intent)
			continue
		}

		promptPath := strings.TrimSuffix(metaPath, "metadata.json") + "prompt.txt"
		prompt, found, err := gitutil.ShowFile(ctx, repo, loc.Rev, loc.Path(promptPath))
		if err != nil {
			return nil, fmt.Errorf("read %s from checkpoint %s: %w", promptPath, checkpointID, err)
		}
		if found && strings.TrimSpace(prompt) != "" {
			intent.Source = IntentSourcePrompt
			intent.Intent = truncateRunes(strings.TrimSpace(prompt), maxPromptIntentBytes)
			intent.Note = "no AI summary on this checkpoint; quoting the session's user prompt"
		} else {
			intent.Note = "checkpoint has neither a summary nor a prompt"
		}
		out = append(out, intent)
	}
	return out, nil
}

// truncateRunes cuts s to at most limit bytes on a rune boundary and marks
// the cut so a quoted prompt is never mistaken for the whole prompt.
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + " [...]"
}

// AttachIntent fills each side's intent list and, for every conflict, picks
// the checkpoint most likely to explain each side of it.
func AttachIntent(ctx context.Context, repo string, report *MergeReport) error {
	a, err := CheckpointIntents(ctx, repo, report.Base, report.A.Ref)
	if err != nil {
		return err
	}
	b, err := CheckpointIntents(ctx, repo, report.Base, report.B.Ref)
	if err != nil {
		return err
	}
	report.A.Intents, report.B.Intents = a, b
	for i := range report.Conflicts {
		c := &report.Conflicts[i]
		c.ChangedIntent = pickIntent(sideIntents(report, c.ChangedOn), c.Changed.Path)
		if c.Consumer != nil {
			c.ConsumerIntent = pickIntent(sideIntents(report, c.ConsumerOn), c.Consumer.Path)
		}
	}
	return nil
}

func sideIntents(report *MergeReport, ref string) []CheckpointIntent {
	if ref == report.A.Ref {
		return report.A.Intents
	}
	return report.B.Intents
}

// pickIntent prefers a checkpoint that lists path among its touched files,
// then the newest checkpoint with a real intent, then the newest of anything.
// Intents arrive newest first from git log.
func pickIntent(intents []CheckpointIntent, path string) *CheckpointIntent {
	for i := range intents {
		for _, file := range intents[i].FilesTouched {
			if file == path {
				chosen := intents[i]
				chosen.Match = "files_touched"
				return &chosen
			}
		}
	}
	for i := range intents {
		if intents[i].Source != IntentSourceMissing {
			chosen := intents[i]
			chosen.Match = "latest"
			return &chosen
		}
	}
	if len(intents) > 0 {
		chosen := intents[0]
		chosen.Match = "latest"
		return &chosen
	}
	return nil
}
