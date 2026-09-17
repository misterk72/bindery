package deluge

import (
	"context"
	"fmt"
	"strings"
)

// Location is where deluged leaves a finished torrent, as DownloadLocation
// works it out.
type Location struct {
	Path string
	// Source names where Path came from, as an English phrase.
	Source string
	// Note and NoteFix are set when a grab will not behave the way the
	// configuration suggests, or when part of the answer could not be checked.
	Note    string
	NoteFix string
}

// DownloadLocation returns where deluged leaves a torrent that Bindery adds
// with label, the label exactly as AddTorrent sends it to label.set_torrent.
//
// Order of precedence matches Deluge: a label whose options apply a move
// completed path wins, then the global move_completed_path when "move
// completed" is on, then download_location. Only the needed keys are
// requested.
//
// The Label plugin only knows lowercase labels and its set_torrent does not
// lowercase what it is given, so a label with capitals is rejected. AddTorrent
// ignores that error, which leaves the torrent unlabelled in the global
// folder; this reports exactly that.
func (c *Client) DownloadLocation(ctx context.Context, label string) (Location, error) {
	var cfg struct {
		DownloadLocation  string `json:"download_location"`
		MoveCompleted     bool   `json:"move_completed"`
		MoveCompletedPath string `json:"move_completed_path"`
	}
	keys := []string{"download_location", "move_completed", "move_completed_path"}
	if err := c.call(ctx, true, "core.get_config_values", []any{keys}, &cfg); err != nil {
		return Location{}, fmt.Errorf("read deluge download location: %w", err)
	}
	loc := Location{Path: strings.TrimSpace(cfg.DownloadLocation), Source: "the client default download location"}
	if cfg.MoveCompleted && strings.TrimSpace(cfg.MoveCompletedPath) != "" {
		loc.Path, loc.Source = strings.TrimSpace(cfg.MoveCompletedPath), "the client default move completed path"
	}

	if label == "" {
		return loc, nil
	}
	if label != strings.ToLower(label) {
		loc.Note = fmt.Sprintf("Deluge only accepts lowercase labels, so grabs with the category %q are not labelled and land in the default folder.", label)
		loc.NoteFix = "Use a lowercase category for this client in Bindery."
		return loc, nil
	}
	var opts struct {
		ApplyMoveCompleted bool   `json:"apply_move_completed"`
		MoveCompleted      bool   `json:"move_completed"`
		MoveCompletedPath  string `json:"move_completed_path"`
	}
	if err := c.call(ctx, true, "label.get_options", []any{label}, &opts); err != nil {
		loc.Note = fmt.Sprintf("Bindery could not read the options of the label %q, so a move path set on that label is not checked.", label)
		loc.NoteFix = "Check the settings of that label in Deluge by hand."
		return loc, nil
	}
	if opts.ApplyMoveCompleted && opts.MoveCompleted && strings.TrimSpace(opts.MoveCompletedPath) != "" {
		loc.Path, loc.Source = strings.TrimSpace(opts.MoveCompletedPath), fmt.Sprintf("the move completed path of the label %q", label)
	}
	return loc, nil
}
