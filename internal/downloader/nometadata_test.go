package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// TestGetStalledTorrents_Transmission_NoMetadata is the reporter's torrent
// (#2709): Transmission has held it for weeks with no file list, no total size
// and no connected peers, and errorString is empty because Transmission does
// not consider any of that an error. It must come back as StallNoMetadata,
// while a healthy torrent that is simply downloading must not appear at all.
func TestGetStalledTorrents_Transmission_NoMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{
				"torrents": []map[string]any{
					// The reported shape.
					{
						"id": 7, "status": 0, "errorString": "",
						"totalSize": 0, "percentDone": 0,
						"metadataPercentComplete": 0, "peersConnected": 0,
					},
					// The same shape, but Transmission is still actively
					// trying (status 4). Just as dead.
					{
						"id": 8, "status": 4, "errorString": "",
						"totalSize": 0, "percentDone": 0,
						"metadataPercentComplete": 0, "peersConnected": 3,
					},
					// Healthy: metadata resolved, downloading.
					{
						"id": 9, "status": 4, "errorString": "",
						"totalSize": 8192, "percentDone": 0.25,
						"metadataPercentComplete": 1, "peersConnected": 12,
					},
					// Healthy: metadata resolved, nothing downloaded yet.
					{
						"id": 10, "status": 4, "errorString": "",
						"totalSize": 8192, "percentDone": 0,
						"metadataPercentComplete": 1, "peersConnected": 1,
					},
					// A real error still reports as the client's own signal.
					{
						"id": 11, "status": 0, "errorString": "tracker error",
						"totalSize": 8192, "percentDone": 0.5,
						"metadataPercentComplete": 1,
					},
				},
			},
			"result": "success",
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}

	stalled, _, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if stalled["7"] != StallNoMetadata {
		t.Errorf("stopped magnet with no metadata: want StallNoMetadata, got %v", stalled["7"])
	}
	if stalled["8"] != StallNoMetadata {
		t.Errorf("downloading magnet with no metadata: want StallNoMetadata, got %v", stalled["8"])
	}
	if _, ok := stalled["9"]; ok {
		t.Error("a healthy downloading torrent must not be reported as stalled")
	}
	if _, ok := stalled["10"]; ok {
		t.Error("a torrent with metadata but no progress yet must not be reported as stalled")
	}
	if stalled["11"] != StallClientReported {
		t.Errorf("errored torrent: want StallClientReported, got %v", stalled["11"])
	}
}

// TestGetStalledTorrents_Transmission_NoMetadataFieldsRequested guards the RPC
// contract: totalSize and metadataPercentComplete have to be asked for, or
// Transmission never sends them and every torrent decodes as "no metadata".
func TestGetStalledTorrents_Transmission_NoMetadataFieldsRequested(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"arguments": map[string]any{"torrents": []map[string]any{}},
			"result":    "success",
		})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "transmission", Host: host, Port: port}
	if _, _, err := GetStalledTorrents(context.Background(), client); err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	for _, field := range []string{"totalSize", "percentDone", "metadataPercentComplete"} {
		if !strings.Contains(body, `"`+field+`"`) {
			t.Errorf("torrent-get must request %q, request was: %s", field, body)
		}
	}
}

// TestGetStalledTorrents_QBittorrent_MetaDL covers qBittorrent's version of the
// same torrent: it parks a magnet awaiting metadata in metaDL, which is not
// stalledDL and so was never reported.
func TestGetStalledTorrents_QBittorrent_MetaDL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"hash": "AAA111", "state": "metaDL", "size": 0, "progress": 0},
				{"hash": "BBB222", "state": "forcedMetaDL", "size": 0, "progress": 0},
				{"hash": "CCC333", "state": "downloading", "size": 4096, "progress": 0.5},
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{
		Type: "qbittorrent", Host: host, Port: port, Username: "u", Password: "p",
	}
	stalled, _, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if stalled["aaa111"] != StallNoMetadata {
		t.Errorf("metaDL: want StallNoMetadata, got %v", stalled["aaa111"])
	}
	if stalled["bbb222"] != StallNoMetadata {
		t.Errorf("forcedMetaDL: want StallNoMetadata, got %v", stalled["bbb222"])
	}
	if _, ok := stalled["ccc333"]; ok {
		t.Error("a healthy downloading torrent must not be reported as stalled")
	}
}

// TestGetStalledTorrents_Deluge_NoMetadata covers Deluge, which folds
// libtorrent's downloading_metadata into the plain Downloading state, so the
// zero total_size is the only thing that tells the two apart.
func TestGetStalledTorrents_Deluge_NoMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		var result any
		switch req.Method {
		case "auth.login", "web.connected":
			result = true
		case "core.get_torrents_status":
			result = map[string]any{
				"aaa111": map[string]any{
					"hash": "aaa111", "state": "Downloading",
					"progress": 0.0, "total_size": 0, "total_done": 0,
				},
				"ccc333": map[string]any{
					"hash": "ccc333", "state": "Downloading",
					"progress": 25.0, "total_size": 4000, "total_done": 1000,
				},
			}
		default:
			t.Fatalf("unexpected method: %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "error": nil, "id": req.ID})
	}))
	defer srv.Close()

	host, port := serverHostPort(t, srv.URL)
	client := &models.DownloadClient{Type: "deluge", Host: host, Port: port, Password: "pw"}
	stalled, _, err := GetStalledTorrents(context.Background(), client)
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if stalled["aaa111"] != StallNoMetadata {
		t.Errorf("magnet with no metadata: want StallNoMetadata, got %v", stalled["aaa111"])
	}
	if _, ok := stalled["ccc333"]; ok {
		t.Error("a healthy downloading torrent must not be reported as stalled")
	}
}

// TestGetStalledTorrents_Rtorrent_NoMetadata covers rTorrent's "<hash>.meta"
// placeholder, which reports a zero d.size_bytes and no message at all.
func TestGetStalledTorrents_Rtorrent_NoMetadata(t *testing.T) {
	stub := newRtorrentSizeStub(t, 0, "0")
	stalled, _, err := GetStalledTorrents(context.Background(), stub.client(t, 210))
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if stalled[rtorrentTestHash] != StallNoMetadata {
		t.Errorf("magnet placeholder: want StallNoMetadata, got %v", stalled[rtorrentTestHash])
	}

	healthy := newRtorrentSizeStub(t, 1000, "0")
	stalled, _, err = GetStalledTorrents(context.Background(), healthy.client(t, 211))
	if err != nil {
		t.Fatalf("GetStalledTorrents: %v", err)
	}
	if len(stalled) != 0 {
		t.Errorf("a healthy downloading torrent must not be reported as stalled, got %v", stalled)
	}
}

// newRtorrentSizeStub is newRtorrentStub with the reported d.size_bytes under
// the test's control, which is what separates a magnet placeholder from a
// torrent whose metadata has arrived.
func newRtorrentSizeStub(t *testing.T, sizeBytes int64, complete string) *rtorrentStub {
	t.Helper()
	s := &rtorrentStub{complete: complete}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml")
		if !strings.Contains(string(body), "d.multicall2") {
			_, _ = io.WriteString(w, `<?xml version="1.0"?><methodResponse><params><param><value><i8>0</i8></value></param></params></methodResponse>`)
			return
		}
		_, _ = io.WriteString(w, fmt.Sprintf(`<?xml version="1.0"?><methodResponse><params><param><value><array><data>
<value><array><data>
<value><string>the-book</string></value>
<value><string>%s</string></value>
<value><string></string></value>
<value><string>/seedbox/downloads</string></value>
<value><string>books</string></value>
<value><i8>%d</i8></value>
<value><i8>0</i8></value>
<value><i8>0</i8></value>
<value><i8>%s</i8></value>
<value><i8>1</i8></value>
<value><i8>1</i8></value>
<value><string></string></value>
</data></array></value>
</data></array></value></param></params></methodResponse>`, strings.ToUpper(rtorrentTestHash), sizeBytes, complete))
	}))
	t.Cleanup(s.Close)
	return s
}
