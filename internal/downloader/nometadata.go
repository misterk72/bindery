package downloader

import (
	"strings"

	"github.com/vavallee/bindery/internal/downloader/deluge"
	"github.com/vavallee/bindery/internal/downloader/qbittorrent"
	"github.com/vavallee/bindery/internal/downloader/rtorrent"
	"github.com/vavallee/bindery/internal/downloader/transmission"
)

// StallKind says why a torrent was reported as stalled, so the caller can
// explain itself to the user rather than writing one generic reason over two
// very different failures.
type StallKind uint8

const (
	// StallNone is the zero value and means "not stalled". It exists so that
	// indexing the map GetStalledTorrents returns with an absent key cannot
	// read as a real stall reason.
	StallNone StallKind = iota
	// StallClientReported is the download client's own signal: qBittorrent's
	// stalledDL, a Transmission torrent stopped with an errorString, a Deluge
	// torrent in the Error state, an rTorrent d.message on an incomplete item.
	StallClientReported
	// StallNoMetadata is a torrent the client accepted and is still holding,
	// but for which it has never resolved the metadata: no file list, no total
	// size, no progress. See the rule below for why this needs to exist.
	StallNoMetadata
)

// String is the short machine readable label that goes in the log line.
func (k StallKind) String() string {
	switch k {
	case StallNoMetadata:
		return "no_metadata"
	case StallClientReported:
		return "client_reported"
	default:
		return "none"
	}
}

// Reason is the user facing sentence stored on the failed download, on the
// blocklist entry and in the history event.
func (k StallKind) Reason() string {
	if k == StallNoMetadata {
		return "stalled: the download client never resolved this magnet's metadata, so it has no files and no size"
	}
	return "stalled: no peers / no download progress"
}

// The "accepted but never resolved" rule (#2709).
//
// A magnet that nobody is serving is accepted by every torrent client and then
// sits there for ever. The client is not in an error state and has not stopped
// trying, so none of the native stall signals fire: Transmission leaves
// errorString empty, qBittorrent parks it in metaDL rather than stalledDL,
// Deluge calls it Downloading, rTorrent keeps it as a .meta placeholder with no
// message. The reporter's torrent had been in that state for 34 days with the
// queue row still reading "downloading".
//
// The shared shape across all four clients is: the client knows nothing about
// the content. No total size, no progress, not complete. That is only ever true
// of a torrent whose metadata has not arrived, because a real torrent's size is
// known the moment its metadata is.
//
// AGE IS NOT CHECKED HERE, AND IT MUST BE CHECKED BY THE CALLER. A healthy
// magnet looks exactly like this for the first few seconds of its life, so on
// its own this rule would fail every magnet the moment it was grabbed. The one
// caller, Scheduler.checkStalledDownloads, only ever looks at downloads whose
// grabbed_at is older than the stall timeout (stall.timeout_minutes, default
// 120 minutes), which is the minimum safe age: two hours of a client trying and
// failing to find a single peer with the metadata is not a slow magnet, it is a
// dead one. Anything reusing these predicates needs the same gate.

// transmissionHasNoMetadata reports whether a Transmission torrent is one the
// daemon has accepted but never resolved.
//
// totalSize is the load-bearing field: Transmission reports 0 until the
// metadata arrives and the real size for ever after, so it does not depend on
// which status the torrent happens to be parked in. The reporter's torrent was
// status 0 (stopped) despite never having been stopped by hand, which is why
// status is deliberately not part of the test.
//
// metadataPercentComplete is the daemon saying the same thing in its own words
// and is only a cross-check: a Transmission too old to report it (pre RPC 14,
// 2013) sends 0, which the totalSize test has already established.
func transmissionHasNoMetadata(t transmission.Torrent) bool {
	return t.TotalSize == 0 && t.PercentDone == 0 && t.MetadataPercentComplete < 1
}

// qbittorrentHasNoMetadata reports whether a qBittorrent torrent is stuck
// fetching metadata. qBittorrent names this state outright, which is why the
// size is only a guard: metaDL is the state a magnet holds from the moment it
// is added until its metadata lands, and forcedMetaDL is the same state on a
// torrent the user forced to run.
func qbittorrentHasNoMetadata(t qbittorrent.Torrent) bool {
	state := strings.ToLower(t.State)
	return (state == "metadl" || state == "forcedmetadl") && t.Size == 0 && t.Progress == 0
}

// delugeHasNoMetadata reports whether a Deluge torrent is stuck fetching
// metadata. Deluge folds libtorrent's downloading_metadata into the plain
// Downloading state, so there is no state string to key on and total_size is
// the only signal. Deluge reports the full content size there regardless of
// which files are selected, so a fully deselected torrent does not land here.
func delugeHasNoMetadata(t deluge.TorrentStatus) bool {
	return strings.EqualFold(t.State, "downloading") && t.TotalSize == 0 && t.Progress == 0
}

// rtorrentHasNoMetadata reports whether an rTorrent item is a magnet
// placeholder that never resolved. rTorrent loads a magnet as a "<hash>.meta"
// item whose d.size_bytes stays 0 until it pulls the metadata off the DHT (see
// Client.Add, which already warns about exactly this window).
func rtorrentHasNoMetadata(t rtorrent.Torrent) bool {
	return t.SizeBytes <= 0 && !t.Complete
}
