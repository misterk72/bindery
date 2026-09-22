package importer

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vavallee/bindery/internal/calibre"
	"github.com/vavallee/bindery/internal/covers"
	"github.com/vavallee/bindery/internal/models"
)

// recordingAdder is a fakeCalibreAdder that can also answer the optional
// capability and metadata-update interfaces the plugin client implements.
type recordingAdder struct {
	mu            sync.Mutex
	calls         []string
	metas         []calibre.Metadata
	nextID        int64
	err           error
	supportsCover bool
	supportsPatch bool
	updates       map[int64]calibre.Metadata
}

func (f *recordingAdder) Add(_ context.Context, path string, meta calibre.Metadata) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, path)
	f.metas = append(f.metas, meta)
	return f.nextID, f.err
}

func (f *recordingAdder) SupportsCover(context.Context) bool { return f.supportsCover }

func (f *recordingAdder) SupportsMetadataUpdate(context.Context) bool { return f.supportsPatch }

func (f *recordingAdder) UpdateMetadata(_ context.Context, id int64, meta calibre.Metadata) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updates == nil {
		f.updates = map[int64]calibre.Metadata{}
	}
	f.updates[id] = meta
	return []string{"series"}, nil
}

func (f *recordingAdder) updateCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates)
}

// TestPushToCalibre_ResolverBuildsAdderPerPush is review item 1. The adder
// used to be built once at boot from the boot-time mode while the scanner was
// handed a live mode resolver, so switching mode in the UI, or fixing
// plugin_url / plugin_api_key / push_path_remap, had no effect until restart.
func TestPushToCalibre_ResolverBuildsAdderPerPush(t *testing.T) {
	s, _, book, author, ctx := importScannerFixture(t)

	cli := &recordingAdder{nextID: 1}
	plugin := &recordingAdder{nextID: 2}
	mode := calibre.ModeCalibredb
	s.WithCalibreResolver(func() calibre.Mode { return mode }, func(m calibre.Mode) calibreAdder {
		if m == calibre.ModePlugin {
			return plugin
		}
		return cli
	})

	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/a.epub")
	mode = calibre.ModePlugin
	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/b.epub")

	if len(cli.calls) != 1 || len(plugin.calls) != 1 {
		t.Fatalf("calibredb calls = %v, plugin calls = %v; want one each", cli.calls, plugin.calls)
	}
	if plugin.calls[0] != "/library/b.epub" {
		t.Errorf("second push went to %q", plugin.calls[0])
	}
}

// TestPushToCalibre_WarnsWhenAdderDisabledWithModeOn keeps the second half of
// item 1: ErrDisabled from an adder while the resolved mode is not off is now
// a real bug rather than a stale boot, and used to return in total silence.
func TestPushToCalibre_WarnsWhenAdderDisabledWithModeOn(t *testing.T) {
	s, _, book, author, ctx := importScannerFixture(t)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s.WithCalibre(modeFn(calibre.ModeCalibredb), &fakeCalibreAdder{err: calibre.ErrDisabled})
	s.pushToCalibre(ctx, book, author, nil, "", "", "/library/a.epub")

	if !strings.Contains(buf.String(), "reports the integration disabled") {
		t.Errorf("log = %q, want a warning naming the mismatch", buf.String())
	}
}

// TestPushToCalibre_RefusesADirectory is review item 7. The audiobook call
// site hands pushToCalibre a folder. The plugin rejects it with 400 and the
// legacy retry rule then sent it a second time, producing two doomed requests
// and one misleading warning per audiobook import.
func TestPushToCalibre_RefusesADirectory(t *testing.T) {
	for _, mode := range []calibre.Mode{calibre.ModePlugin, calibre.ModeCalibredb} {
		t.Run(string(mode), func(t *testing.T) {
			s, _, book, author, ctx := importScannerFixture(t)
			dir := t.TempDir()
			fc := &fakeCalibreAdder{nextID: 1}
			s.WithCalibre(modeFn(mode), fc)

			s.pushToCalibre(ctx, book, author, nil, "", "", dir)

			if len(fc.calls) != 0 {
				t.Errorf("Add called with a directory: %v", fc.calls)
			}
		})
	}
}

func TestPushToCalibre_StillPushesARegularFile(t *testing.T) {
	s, _, book, author, ctx := importScannerFixture(t)
	path := filepath.Join(t.TempDir(), "a.epub")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCalibreAdder{nextID: 1}
	s.WithCalibre(modeFn(calibre.ModePlugin), fc)

	s.pushToCalibre(ctx, book, author, nil, "", "", path)

	if len(fc.calls) != 1 {
		t.Errorf("Add calls = %v, want one", fc.calls)
	}
}

// TestCalibreMetadata_SendsCoverInPluginMode is review item 9's Bindery half:
// the cover was gated to calibredb mode, so a plugin-mode book only ever
// showed whatever artwork was embedded in the epub.
func TestCalibreMetadata_SendsCoverInPluginMode(t *testing.T) {
	s, _, book, author, ctx := importScannerFixture(t)
	store := covers.NewStore(t.TempDir())
	src := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(src, jpegBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(src)
	if err != nil {
		t.Fatalf("store cover: %v", err)
	}
	s.WithCoverStore(store)
	book.ImageURL = ref

	adder := &recordingAdder{nextID: 1, supportsCover: true}
	s.WithCalibre(modeFn(calibre.ModePlugin), adder)
	s.pushToCalibre(ctx, book, author, nil, "", "", tempEbook(t, "a.epub"))

	if len(adder.metas) != 1 {
		t.Fatalf("metas = %v", adder.metas)
	}
	got := adder.metas[0].CoverPath
	if got == "" {
		t.Fatal("coverPath is empty, want a real path")
	}
	if strings.HasPrefix(got, "bindery-cover:") {
		t.Errorf("coverPath = %q, want a filesystem path rather than a reference", got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("coverPath %q does not exist: %v", got, err)
	}
}

// TestCalibreMetadata_OmitsCoverWithoutTheCapability holds the compatibility
// rule: an older plugin that does not advertise `cover` gets no coverPath.
func TestCalibreMetadata_OmitsCoverWithoutTheCapability(t *testing.T) {
	s, _, book, author, ctx := importScannerFixture(t)
	store := covers.NewStore(t.TempDir())
	src := filepath.Join(t.TempDir(), "cover.jpg")
	if err := os.WriteFile(src, jpegBytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(src)
	if err != nil {
		t.Fatal(err)
	}
	s.WithCoverStore(store)
	book.ImageURL = ref

	adder := &recordingAdder{nextID: 1, supportsCover: false}
	s.WithCalibre(modeFn(calibre.ModePlugin), adder)
	s.pushToCalibre(ctx, book, author, nil, "", "", tempEbook(t, "a.epub"))

	if adder.metas[0].CoverPath != "" {
		t.Errorf("coverPath = %q, want it omitted", adder.metas[0].CoverPath)
	}
}

// TestPushToCalibre_409UpdatesARowBinderyOwns is review item 13. A 409 used to
// be a dead end: Bindery recorded the id and stopped, so a book pushed before
// its metadata improved was never corrected.
func TestPushToCalibre_409UpdatesARowBinderyOwns(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	if err := bookRepo.SetCalibreID(ctx, book.ID, 55); err != nil {
		t.Fatal(err)
	}
	existing := int64(55)
	book.CalibreID = &existing

	adder := &recordingAdder{nextID: 55, err: calibre.ErrAlreadyInCalibre, supportsPatch: true}
	s.WithCalibre(modeFn(calibre.ModePlugin), adder)
	s.pushToCalibre(ctx, book, author, nil, "Dune Chronicles", "1", tempEbook(t, "a.epub"))

	if adder.updateCount() != 1 {
		t.Fatalf("update calls = %d, want 1", adder.updateCount())
	}
	if adder.updates[55].Series != "Dune Chronicles" {
		t.Errorf("update payload = %+v, want the current metadata", adder.updates[55])
	}
}

// TestPushToCalibre_409LeavesAnUnknownRowAlone is the conservative half: the
// first time Bindery meets a Calibre row it did not create, it records the
// linkage and writes nothing, because that row may be one a user curated by
// hand.
func TestPushToCalibre_409LeavesAnUnknownRowAlone(t *testing.T) {
	s, bookRepo, book, author, ctx := importScannerFixture(t)
	adder := &recordingAdder{nextID: 77, err: calibre.ErrAlreadyInCalibre, supportsPatch: true}
	s.WithCalibre(modeFn(calibre.ModePlugin), adder)

	s.pushToCalibre(ctx, book, author, nil, "", "", tempEbook(t, "a.epub"))

	if adder.updateCount() != 0 {
		t.Errorf("update calls = %d, want 0 for a row Bindery has not claimed", adder.updateCount())
	}
	got, _ := bookRepo.GetByID(ctx, book.ID)
	if got.CalibreID == nil || *got.CalibreID != 77 {
		t.Errorf("calibre_id = %v, want the 409 id persisted", got.CalibreID)
	}
}

// TestPushToCalibre_409SkipsUpdateWithoutTheCapability holds the compatibility
// rule for the update path.
func TestPushToCalibre_409SkipsUpdateWithoutTheCapability(t *testing.T) {
	s, _, book, author, ctx := importScannerFixture(t)
	existing := int64(55)
	book.CalibreID = &existing
	adder := &recordingAdder{nextID: 55, err: calibre.ErrAlreadyInCalibre, supportsPatch: false}
	s.WithCalibre(modeFn(calibre.ModePlugin), adder)

	s.pushToCalibre(ctx, book, author, nil, "", "", tempEbook(t, "a.epub"))

	if adder.updateCount() != 0 {
		t.Errorf("update calls = %d, want 0 against a plugin that cannot update", adder.updateCount())
	}
}

func tempEbook(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// jpegBytes is the smallest byte sequence the cover store accepts as a JPEG.
func jpegBytes() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, bytes.Repeat([]byte{0x00}, 32)...)
}

var _ = models.Book{}
