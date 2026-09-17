package deluge

import (
	"context"
	"fmt"
	"strings"
)

// DownloadLocation returns the folder deluged leaves a torrent in once it
// finishes, for a torrent Bindery labels with label, plus a phrase naming
// where that came from and a note when part of the answer could not be
// checked.
//
// Order of precedence matches Deluge: a label whose options apply a move
// completed path wins, then the global move_completed_path when "move
// completed" is on, then download_location. Only the needed keys are
// requested.
func (c *Client) DownloadLocation(ctx context.Context, label string) (path, source, note string, err error) {
	var cfg struct {
		DownloadLocation  string `json:"download_location"`
		MoveCompleted     bool   `json:"move_completed"`
		MoveCompletedPath string `json:"move_completed_path"`
	}
	keys := []string{"download_location", "move_completed", "move_completed_path"}
	if err := c.call(ctx, true, "core.get_config_values", []any{keys}, &cfg); err != nil {
		return "", "", "", fmt.Errorf("read deluge download location: %w", err)
	}
	path, source = strings.TrimSpace(cfg.DownloadLocation), "the client default download location"
	if cfg.MoveCompleted && strings.TrimSpace(cfg.MoveCompletedPath) != "" {
		path, source = strings.TrimSpace(cfg.MoveCompletedPath), "the client default move completed path"
	}

	// The Label plugin stores labels in lower case.
	label = strings.ToLower(strings.TrimSpace(label))
	if label == "" {
		return path, source, "", nil
	}
	var opts struct {
		ApplyMoveCompleted bool   `json:"apply_move_completed"`
		MoveCompleted      bool   `json:"move_completed"`
		MoveCompletedPath  string `json:"move_completed_path"`
	}
	if err := c.call(ctx, true, "label.get_options", []any{label}, &opts); err != nil {
		return path, source, fmt.Sprintf("Bindery could not read the options of the label %q, so a move path set on that label is not checked.", label), nil
	}
	if opts.ApplyMoveCompleted && opts.MoveCompleted && strings.TrimSpace(opts.MoveCompletedPath) != "" {
		return strings.TrimSpace(opts.MoveCompletedPath), fmt.Sprintf("the move completed path of the label %q", label), "", nil
	}
	return path, source, "", nil
}
