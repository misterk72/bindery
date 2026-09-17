package deluge

import (
	"context"
	"fmt"
	"strings"
)

// DownloadLocation returns the folder deluged leaves finished torrents in:
// move_completed_path when "move completed" is on, otherwise
// download_location. Only those three keys are requested, so nothing else from
// the daemon config (which holds no secrets today, but may later) is read.
func (c *Client) DownloadLocation(ctx context.Context) (string, error) {
	var cfg struct {
		DownloadLocation  string `json:"download_location"`
		MoveCompleted     bool   `json:"move_completed"`
		MoveCompletedPath string `json:"move_completed_path"`
	}
	keys := []string{"download_location", "move_completed", "move_completed_path"}
	if err := c.call(ctx, true, "core.get_config_values", []any{keys}, &cfg); err != nil {
		return "", fmt.Errorf("read deluge download location: %w", err)
	}
	if cfg.MoveCompleted && strings.TrimSpace(cfg.MoveCompletedPath) != "" {
		return strings.TrimSpace(cfg.MoveCompletedPath), nil
	}
	return strings.TrimSpace(cfg.DownloadLocation), nil
}
