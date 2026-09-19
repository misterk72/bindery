package scheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/indexer"
	"github.com/vavallee/bindery/internal/models"
)

// transmissionNoMetadataFake is a Transmission RPC endpoint that reports one
// torrent in the shape reported in #2709: accepted, still present, zero files,
// zero total size, no connected peers, empty errorString. It records the
// torrent-remove calls so a test can assert the stall handler cleaned up.
type transmissionNoMetadataFake struct {
	*httptest.Server
	mu      sync.Mutex
	removed []int64
}

func newTransmissionNoMetadataFake(t *testing.T, torrentID int64, totalSize int64) *transmissionNoMetadataFake {
	t.Helper()
	f := &transmissionNoMetadataFake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method    string `json:"method"`
			Arguments struct {
				IDs []int64 `json:"ids"`
			} `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "torrent-remove":
			f.mu.Lock()
			f.removed = append(f.removed, req.Arguments.IDs...)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success"})
		case "torrent-get":
			metadataPct := 0.0
			if totalSize > 0 {
				metadataPct = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"arguments": map[string]any{
					"torrents": []map[string]any{{
						"id": torrentID, "status": 0, "errorString": "",
						"totalSize": totalSize, "percentDone": 0,
						"metadataPercentComplete": metadataPct,
						"peersConnected":          0,
					}},
				},
				"result": "success",
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success"})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *transmissionNoMetadataFake) removals() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.removed...)
}

// noMetadataFixture builds an install with one Transmission client and one
// grabbed torrent download, and returns the repos the assertions need.
type noMetadataFixture struct {
	scheduler *Scheduler
	downloads *db.DownloadRepo
	blocklist *db.BlocklistRepo
	history   *db.HistoryRepo
	guid      string
}

func newNoMetadataFixture(t *testing.T, srvURL string, grabbedAgo time.Duration) *noMetadataFixture {
	t.Helper()
	host, port := stallServerHostPort(t, srvURL)

	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()

	authorsRepo := db.NewAuthorRepo(database)
	booksRepo := db.NewBookRepo(database)
	author := &models.Author{ForeignID: "OLMETA", Name: "Meta", SortName: "Meta", MetadataProvider: "ol", Monitored: true}
	if err := authorsRepo.Create(ctx, author); err != nil {
		t.Fatalf("create author: %v", err)
	}
	book := &models.Book{
		ForeignID: "OLBMETA", AuthorID: author.ID, Title: "Unresolved Book",
		SortTitle: "Unresolved Book", Status: models.BookStatusWanted,
		Genres: []string{}, MetadataProvider: "ol", Monitored: true,
	}
	if err := booksRepo.Create(ctx, book); err != nil {
		t.Fatalf("create book: %v", err)
	}

	clientsRepo := db.NewDownloadClientRepo(database)
	client := &models.DownloadClient{
		Name: "transmission", Type: "transmission", Host: host, Port: port, Enabled: true,
	}
	if err := clientsRepo.Create(ctx, client); err != nil {
		t.Fatalf("create client: %v", err)
	}

	indexersRepo := db.NewIndexerRepo(database)
	idx := &models.Indexer{Name: "mock", Type: "newznab", URL: "http://x", APIKey: "k", Enabled: true, Priority: 25}
	if err := indexersRepo.Create(ctx, idx); err != nil {
		t.Fatalf("create indexer: %v", err)
	}

	downloadsRepo := db.NewDownloadRepo(database)
	tid := "7"
	dl := &models.Download{
		GUID: "g-nometa", Title: "Unresolved Release", BookID: &book.ID,
		IndexerID: &idx.ID, DownloadClientID: &client.ID,
		Status: models.StateGrabbed, Protocol: "torrent",
		TorrentID: &tid,
	}
	if err := downloadsRepo.Create(ctx, dl); err != nil {
		t.Fatalf("create download: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		"UPDATE downloads SET status=?, grabbed_at=? WHERE id=?",
		models.DownloadStatusDownloading, time.Now().UTC().Add(-grabbedAgo), dl.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	settingsRepo := db.NewSettingsRepo(database)
	// Keep the re-search goroutine out of the test; the grab path has its own.
	_ = settingsRepo.Set(ctx, "autoGrab.enabled", "false")

	blocklistRepo := db.NewBlocklistRepo(database)
	historyRepo := db.NewHistoryRepo(database)

	return &noMetadataFixture{
		scheduler: &Scheduler{
			downloads: downloadsRepo,
			clients:   clientsRepo,
			indexers:  indexersRepo,
			books:     booksRepo,
			authors:   authorsRepo,
			settings:  settingsRepo,
			blocklist: blocklistRepo,
			history:   historyRepo,
			searcher:  indexer.NewSearcher(),
		},
		downloads: downloadsRepo,
		blocklist: blocklistRepo,
		history:   historyRepo,
		guid:      "g-nometa",
	}
}

// TestCheckStalledDownloads_TransmissionNoMetadata is #2709 end to end: a
// magnet Transmission accepted and never resolved, older than the stall
// timeout, is failed, removed from the client, blocklisted and given a history
// event, with an error message that says what happened.
func TestCheckStalledDownloads_TransmissionNoMetadata(t *testing.T) {
	fake := newTransmissionNoMetadataFake(t, 7, 0)
	fx := newNoMetadataFixture(t, fake.URL, 34*24*time.Hour)
	ctx := context.Background()

	fx.scheduler.checkStalledDownloads(ctx)

	got, err := fx.downloads.GetByGUID(ctx, fx.guid)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.DownloadStatusFailed {
		t.Errorf("download status: want %q, got %q", models.DownloadStatusFailed, got.Status)
	}
	if !strings.Contains(got.ErrorMessage, "metadata") {
		t.Errorf("error message should say the metadata never resolved, got %q", got.ErrorMessage)
	}

	if removed := fake.removals(); len(removed) != 1 || removed[0] != 7 {
		t.Errorf("expected the empty torrent to be removed from Transmission, got %v", removed)
	}

	blocked, err := fx.blocklist.IsBlocked(ctx, fx.guid)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked {
		t.Error("expected the release to be blocklisted so the next search picks a different one")
	}

	events, err := fx.history.ListByType(ctx, models.HistoryEventDownloadStalled)
	if err != nil {
		t.Fatalf("ListByType: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 downloadStalled history event, got %d", len(events))
	}
}

// TestCheckStalledDownloads_NoMetadataYoungerThanTimeout is the false positive
// guard. A magnet that has only just been grabbed looks exactly like a dead
// one: zero files, zero size, no peers. Nothing may happen to it until it has
// had the whole stall timeout to resolve.
func TestCheckStalledDownloads_NoMetadataYoungerThanTimeout(t *testing.T) {
	fake := newTransmissionNoMetadataFake(t, 7, 0)
	fx := newNoMetadataFixture(t, fake.URL, 30*time.Minute) // default timeout is 120m
	ctx := context.Background()

	fx.scheduler.checkStalledDownloads(ctx)

	got, err := fx.downloads.GetByGUID(ctx, fx.guid)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.DownloadStatusDownloading {
		t.Errorf("a magnet still inside the stall timeout must be left alone, got status %q", got.Status)
	}
	if len(fake.removals()) != 0 {
		t.Errorf("nothing should have been removed from the client, got %v", fake.removals())
	}
	blocked, _ := fx.blocklist.IsBlocked(ctx, fx.guid)
	if blocked {
		t.Error("a magnet still inside the stall timeout must not be blocklisted")
	}
}

// TestCheckStalledDownloads_HealthyTransmissionDownloadUntouched pins the other
// side of the rule: a torrent whose metadata resolved is not stalled, however
// long it has been going.
func TestCheckStalledDownloads_HealthyTransmissionDownloadUntouched(t *testing.T) {
	fake := newTransmissionNoMetadataFake(t, 7, 8192)
	fx := newNoMetadataFixture(t, fake.URL, 34*24*time.Hour)
	ctx := context.Background()

	fx.scheduler.checkStalledDownloads(ctx)

	got, err := fx.downloads.GetByGUID(ctx, fx.guid)
	if err != nil {
		t.Fatalf("GetByGUID: %v", err)
	}
	if got.Status != models.DownloadStatusDownloading {
		t.Errorf("a torrent with metadata must be left alone, got status %q", got.Status)
	}
	if len(fake.removals()) != 0 {
		t.Errorf("nothing should have been removed from the client, got %v", fake.removals())
	}
}
