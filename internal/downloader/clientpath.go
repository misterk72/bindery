package downloader

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// ClientPathInfo is where a download client says it puts completed downloads.
type ClientPathInfo struct {
	// Path is the folder exactly as the client reports it, in the client's own
	// filesystem namespace. Empty when the client exposes no usable folder.
	Path string
	// Source names the client setting Path came from, as an English phrase
	// ("category save path", "complete_dir") for use in a sentence.
	Source string
}

// ClientTypeName is the display name for a download client type.
func ClientTypeName(clientType string) string {
	switch clientType {
	case "qbittorrent":
		return "qBittorrent"
	case "transmission":
		return "Transmission"
	case "deluge":
		return "Deluge"
	case "rtorrent":
		return "rTorrent"
	case "nzbget":
		return "NZBGet"
	default:
		return "SABnzbd"
	}
}

// CompletedPath asks the client where it puts completed downloads for the
// client's configured category. It is the one place each client type's answer
// is worked out, shared by the Test button's visibility check and the diagnose
// action.
//
// An error means the client would not answer (for SABnzbd, typically an NZB
// only API key). A zero Path with no error means the client answered but has
// no usable folder: no category, a category the client does not know, or a
// relative folder Bindery cannot anchor.
func CompletedPath(ctx context.Context, client *models.DownloadClient) (ClientPathInfo, error) {
	if client == nil {
		return ClientPathInfo{}, nil
	}
	switch client.Type {
	case "qbittorrent":
		category := strings.TrimSpace(client.Category)
		if category == "" {
			return ClientPathInfo{}, nil
		}
		qb := QbittorrentFor(client)
		categories, err := qb.GetCategories(ctx)
		if err != nil {
			return ClientPathInfo{}, err
		}
		qbCategory, ok := categories[category]
		if !ok {
			return ClientPathInfo{}, nil
		}
		if savePath := strings.TrimSpace(qbCategory.SavePath); savePath != "" {
			return ClientPathInfo{Path: savePath, Source: "category save path"}, nil
		}
		if defaultPath, err := qb.GetDefaultSavePath(ctx); err == nil && strings.TrimSpace(defaultPath) != "" {
			return ClientPathInfo{Path: strings.TrimSpace(defaultPath), Source: "default save path"}, nil
		}
		return ClientPathInfo{}, nil
	case "nzbget":
		dir, err := NzbgetFor(client).CompletedDir(ctx, client.Category)
		if err != nil {
			return ClientPathInfo{}, err
		}
		return ClientPathInfo{Path: strings.TrimSpace(dir), Source: "DestDir"}, nil
	case "rtorrent":
		dir, err := RtorrentFor(client).DefaultDirectory(ctx)
		if err != nil {
			return ClientPathInfo{}, err
		}
		return ClientPathInfo{Path: strings.TrimSpace(dir), Source: "directory.default"}, nil
	case "transmission":
		// SendDownload passes an absolute Category as the torrent's
		// download-dir, so that is where the files go when it is set.
		if category := strings.TrimSpace(client.Category); strings.HasPrefix(category, "/") {
			return ClientPathInfo{Path: category, Source: "category folder"}, nil
		}
		dir, err := TransmissionFor(client).DownloadDir(ctx)
		if err != nil {
			return ClientPathInfo{}, err
		}
		return ClientPathInfo{Path: dir, Source: "download-dir"}, nil
	case "deluge":
		dir, err := DelugeFor(client).DownloadLocation(ctx)
		if err != nil {
			return ClientPathInfo{}, err
		}
		return ClientPathInfo{Path: dir, Source: "download location"}, nil
	default:
		dir, err := SabnzbdFor(client).CompleteDir(ctx, client.Category)
		if err != nil {
			return ClientPathInfo{}, err
		}
		return ClientPathInfo{Path: dir, Source: "completed folder"}, nil
	}
}

// TestConnection checks the client answers with the stored credentials and
// nothing more. TestClient also validates SABnzbd and NZBGet categories, which
// the diagnose action reports as a separate check so a missing category is not
// shown as a connection failure.
func TestConnection(ctx context.Context, client *models.DownloadClient) error {
	switch client.Type {
	case "nzbget":
		return NzbgetFor(client).Test(ctx)
	case "qbittorrent", "transmission", "deluge", "rtorrent":
		return TestClient(ctx, client)
	default:
		return SabnzbdFor(client).Test(ctx)
	}
}

// CategoryReport is the result of CheckCategories.
type CategoryReport struct {
	// Checked is false for client types without a category list Bindery can
	// read (Transmission, Deluge, rTorrent).
	Checked bool
	// Wanted is the distinct non-empty categories configured in Bindery.
	Wanted []string
	// Missing is the subset of Wanted the client does not define.
	Missing []string
	// Existing is every category the client defines, sorted.
	Existing []string
}

// CheckCategories compares the categories configured in Bindery with the ones
// the client defines. Matching is exact, as it is at grab time; a nested
// qBittorrent category such as "books/ebooks" is its own key and matches.
func CheckCategories(ctx context.Context, client *models.DownloadClient) (CategoryReport, error) {
	report := CategoryReport{}
	for _, c := range []string{client.Category, client.CategoryAudiobook} {
		c = strings.TrimSpace(c)
		if c != "" && !slices.Contains(report.Wanted, c) {
			report.Wanted = append(report.Wanted, c)
		}
	}
	var existing []string
	switch client.Type {
	case "qbittorrent":
		cats, err := QbittorrentFor(client).GetCategories(ctx)
		if err != nil {
			return report, err
		}
		for name := range cats {
			existing = append(existing, name)
		}
	case "nzbget":
		cats, err := NzbgetFor(client).ListCategories(ctx)
		if err != nil {
			return report, err
		}
		existing = cats
	case "transmission", "deluge", "rtorrent":
		return report, nil
	default:
		cats, err := SabnzbdFor(client).GetCategories(ctx)
		if err != nil {
			return report, err
		}
		existing = cats
	}
	report.Checked = true
	sort.Strings(existing)
	report.Existing = existing
	for _, w := range report.Wanted {
		if !slices.Contains(existing, w) {
			report.Missing = append(report.Missing, w)
		}
	}
	return report, nil
}

// FindCaseInsensitivePathUnder is findCaseInsensitivePath confined to base: it
// lists only base and folders beneath it, never an ancestor, and it does not
// follow a symlink out of that tree. p must be at or
// under base, and base must exist, or it reports nothing. The diagnose action
// uses this form because the path came from a download client.
func FindCaseInsensitivePathUnder(base, p string) (resolved, divergedAt string) {
	base, p = filepath.Clean(base), filepath.Clean(p)
	if !filepath.IsAbs(base) || !PathIsAtOrUnder(p, base) {
		return "", ""
	}
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		return "", ""
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p, base), string(filepath.Separator))
	return walkCaseInsensitive(base, rest, false)
}

// PathIsAtOrUnder reports whether candidate is base or lies beneath it. Both
// must already be filepath.Clean'd.
func PathIsAtOrUnder(candidate, base string) bool {
	return pathIsAtOrUnder(candidate, base)
}
