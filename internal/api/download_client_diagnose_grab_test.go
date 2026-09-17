package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
)

// qbitGrabServer is a qBittorrent fake with a configurable category map and
// default save path.
func qbitGrabServer(t *testing.T, categories map[string]string, defaultSavePath string) (string, int) {
	t.Helper()
	cats := map[string]map[string]string{}
	for name, savePath := range categories {
		cats[name] = map[string]string{"name": name, "savePath": savePath}
	}
	catsJSON, _ := json.Marshal(cats)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("5.1.4"))
		case "/api/v2/torrents/categories":
			_, _ = w.Write(catsJSON)
		case "/api/v2/app/defaultSavePath":
			_, _ = w.Write([]byte(defaultSavePath))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return serverAddr(t, srv.URL)
}

// TestDiagnose_QbittorrentEmptyCategorySavePathUsesDefaultPlusCategory: with
// automatic torrent management on, a category with no save path saves to the
// default save path plus the category name, not to the default save path.
func TestDiagnose_QbittorrentEmptyCategorySavePathUsesDefaultPlusCategory(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Mkdir(filepath.Join(downloads, "books"), 0o750); err != nil {
		t.Fatal(err)
	}
	host, port := qbitGrabServer(t, map[string]string{"books": ""}, "/remote")
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "books", "/remote:"+downloads))
	path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	if p := resp.Paths[0]; p.ClientPath != "/remote/books" || p.LocalPath != filepath.Join(downloads, "books") {
		t.Errorf("paths = %+v, want the default save path plus the category", resp.Paths)
	}
	if !strings.Contains(path.Message, "the client default save path plus the category name") {
		t.Errorf("message should name where the path came from: %q", path.Message)
	}
}

// TestDiagnose_QbittorrentNoCategoryUsesTheSavePathBinderySends: without a
// category Bindery sends an explicit save path, so that is the folder to check.
func TestDiagnose_QbittorrentNoCategoryUsesTheSavePathBinderySends(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	host, port := qbitGrabServer(t, map[string]string{}, "/somewhere/else")
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "", "/qbit/downloads:"+downloads))
	path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	if !strings.Contains(path.Message, "the save path Bindery sends") {
		t.Errorf("message = %q", path.Message)
	}
	if p := resp.Paths[0]; p.ClientPath != "/qbit/downloads" || p.LocalPath != downloads {
		t.Errorf("paths = %+v", resp.Paths)
	}
	wantDiagStatus(t, resp, diagCodeLocalPath, diagPass)
}

// TestDiagnose_ChecksEbookAndAudiobookFolders: grabs resolve their category
// per media type, so a broken audiobook folder must show up even while the
// ebook folder works.
func TestDiagnose_ChecksEbookAndAudiobookFolders(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Mkdir(filepath.Join(downloads, "books"), 0o750); err != nil {
		t.Fatal(err)
	}
	host, port := qbitGrabServer(t, map[string]string{"books": "/remote/books", "audio": "/remote/audio"}, "/remote")
	client := qbitClient(host, port, "books", "/remote:"+downloads)
	client.CategoryAudiobook = "audio"
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, client)

	if len(resp.Paths) != 2 || resp.Paths[0].MediaType != models.MediaTypeEbook || resp.Paths[1].MediaType != models.MediaTypeAudiobook {
		t.Fatalf("paths = %+v, want an ebook row and an audiobook row", resp.Paths)
	}
	var ebookLocal, audioLocal *diagCheckResult
	for i := range resp.Checks {
		c := &resp.Checks[i]
		if c.Code != diagCodeLocalPath {
			continue
		}
		switch c.MediaType {
		case models.MediaTypeEbook:
			ebookLocal = c
		case models.MediaTypeAudiobook:
			audioLocal = c
		}
	}
	if ebookLocal == nil || ebookLocal.Status != diagPass {
		t.Errorf("ebook local_path = %+v, want pass", ebookLocal)
	}
	if audioLocal == nil || audioLocal.Status != diagFail || !strings.Contains(audioLocal.Message, "audio") {
		t.Errorf("audiobook local_path = %+v, want a failure naming the audio folder", audioLocal)
	}
}

// TestDiagnose_NzbgetAppendCategoryDir: a category with no DestDir of its own
// lands in DestDir/<category> while AppendCategoryDir is on (the default).
func TestDiagnose_NzbgetAppendCategoryDir(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	if err := os.Mkdir(filepath.Join(downloads, "Books"), 0o750); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"version"`) {
			_, _ = w.Write([]byte(`{"version":"1.1","result":"21.0"}`))
			return
		}
		fmt.Fprintf(w, `{"version":"1.1","result":[{"Name":"MainDir","Value":%q},{"Name":"DestDir","Value":"${MainDir}"},{"Name":"Server1.Password","Value":"news-SECRET-pass"},{"Name":"Category1.Name","Value":"Books"},{"Name":"Category1.DestDir","Value":""}]}`, downloads)
	}))
	defer srv.Close()
	host, port := serverAddr(t, srv.URL)
	resp, raw := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
		Name: "NZBGet", Type: "nzbget", Host: host, Port: port, Category: "Books", Enabled: true,
	})
	path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
	if want := filepath.Join(downloads, "Books"); resp.Paths[0].ClientPath != want {
		t.Errorf("clientPath = %q, want %q", resp.Paths[0].ClientPath, want)
	}
	if !strings.Contains(path.Message, "AppendCategoryDir") {
		t.Errorf("message = %q", path.Message)
	}
	if strings.Contains(raw, "news-SECRET-pass") {
		t.Errorf("response carries the NZBGet server password")
	}
}

// TestDiagnose_WindowsPathOnWindowsIsCheckedDirectly: a drive path needs no
// remap when Bindery itself runs on Windows.
func TestDiagnose_WindowsPathOnWindowsIsCheckedDirectly(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	host, port := qbitDiagServer(t, qbitCategory("books", `D:\Torrents\books`))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: "D:/Torrents", goos: "windows"}, qbitClient(host, port, "books", ""))
	wantDiagStatus(t, resp, diagCodeRemap, diagPass)
	local := diagCheckByCode(t, resp, diagCodeLocalPath)
	if local.Status == diagSkipped || strings.Contains(local.Message, "outside every folder") {
		t.Errorf("local_path = %+v, want the drive path checked as a local folder", local)
	}
}

// TestDiagnose_DanglingSymlinkIsNotFollowed: downloads/dangling points at a
// folder outside every configured root that does not exist yet. Bindery must
// not follow it, and must not create anything outside, before or after the
// target appears.
func TestDiagnose_DanglingSymlinkIsNotFollowed(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "notyet")
	link := filepath.Join(downloads, "dangling")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(outside, past, past); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	probes := 0
	setup := diagnoseSetup{
		downloadDir: downloads, roots: []string{t.TempDir()},
		probe: func(string, string) (bool, string) { mu.Lock(); probes++; mu.Unlock(); return true, "" },
	}
	host, port := qbitDiagServer(t, qbitCategory("books", link))

	resp, _ := runDiagnose(t, setup, qbitClient(host, port, "books", ""))
	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if !strings.Contains(local.Message, "symbolic link that does not resolve") {
		t.Errorf("message = %q", local.Message)
	}
	if info, err := os.Stat(outside); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("outside folder changed while the link dangled")
	}

	// The target appears. The link now leads outside and is still refused.
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(target, past, past); err != nil {
		t.Fatal(err)
	}
	resp, _ = runDiagnose(t, setup, qbitClient(host, port, "books", ""))
	wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if info, err := os.Stat(target); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("something was created in the link target once it appeared")
	}
	mu.Lock()
	defer mu.Unlock()
	if probes != 0 {
		t.Errorf("hardlink probe ran %d times through the link", probes)
	}
}

// TestDiagnose_PrefixConfusionIsOutside: "<download>-evil" shares a string
// prefix with the download folder but is not under it.
func TestDiagnose_PrefixConfusionIsOutside(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	evil := downloads + "-evil"
	if err := os.Mkdir(evil, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(evil) })
	past := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(evil, past, past); err != nil {
		t.Fatal(err)
	}
	host, port := qbitDiagServer(t, qbitCategory("books", evil))
	resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, qbitClient(host, port, "books", ""))
	local := wantDiagStatus(t, resp, diagCodeLocalPath, diagFail)
	if !strings.Contains(local.Message, "outside every folder") {
		t.Errorf("message = %q", local.Message)
	}
	if info, err := os.Stat(evil); err != nil || !info.ModTime().Equal(past) {
		t.Errorf("something was created in the prefix confused folder")
	}
}

// TestDiagnose_SlowFilesystemAnswersUnknown: a probe that blocks past the
// filesystem deadline, as on a dead network mount, returns unknown instead of
// holding the request.
func TestDiagnose_SlowFilesystemAnswersUnknown(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()
	host, port := qbitDiagServer(t, qbitCategory("books", downloads))
	start := time.Now()
	resp, _ := runDiagnose(t, diagnoseSetup{
		downloadDir: downloads, roots: []string{t.TempDir()}, fsTimeout: 50 * time.Millisecond,
		probe: func(string, string) (bool, string) { time.Sleep(time.Second); return true, "" },
	}, qbitClient(host, port, "books", ""))
	links := wantDiagStatus(t, resp, diagCodeHardlinks, diagUnknown)
	if !strings.Contains(links.Message, "did not respond") {
		t.Errorf("message = %q", links.Message)
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Errorf("Diagnose took %v, want it to return at the filesystem deadline", elapsed)
	}
}

func delugeLabelServer(t *testing.T, downloads string, labelOptions string) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login", "web.connected":
			fmt.Fprintf(w, `{"result":true,"error":null,"id":%d}`, req.ID)
		case "core.get_config_values":
			fmt.Fprintf(w, `{"result":{"download_location":"/incomplete","move_completed":false,"move_completed_path":""},"error":null,"id":%d}`, req.ID)
		case "label.get_options":
			if labelOptions == "" {
				fmt.Fprintf(w, `{"result":null,"error":{"code":2,"message":"Unknown method"},"id":%d}`, req.ID)
				return
			}
			fmt.Fprintf(w, `{"result":%s,"error":null,"id":%d}`, labelOptions, req.ID)
		default:
			fmt.Fprintf(w, `{"result":null,"error":null,"id":%d}`, req.ID)
		}
	}))
	t.Cleanup(srv.Close)
	return serverAddr(t, srv.URL)
}

func TestDiagnose_DelugeLabelMovePath(t *testing.T) {
	defer httpsec.AllowLoopbackForTests()()
	downloads := t.TempDir()

	t.Run("label move path wins", func(t *testing.T) {
		opts, _ := json.Marshal(map[string]any{"apply_move_completed": true, "move_completed": true, "move_completed_path": downloads})
		host, port := delugeLabelServer(t, downloads, string(opts))
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
			Name: "Deluge", Type: "deluge", Host: host, Port: port, Password: "deluge", Category: "Books", Enabled: true,
		})
		path := wantDiagStatus(t, resp, diagCodeClientPath, diagPass)
		if resp.Paths[0].ClientPath != downloads || !strings.Contains(path.Message, `label "books"`) {
			t.Errorf("paths = %+v, message = %q", resp.Paths, path.Message)
		}
	})

	t.Run("unreadable label warns", func(t *testing.T) {
		host, port := delugeLabelServer(t, downloads, "")
		resp, _ := runDiagnose(t, diagnoseSetup{downloadDir: downloads}, &models.DownloadClient{
			Name: "Deluge", Type: "deluge", Host: host, Port: port, Password: "deluge", Category: "books", Enabled: true,
		})
		path := wantDiagStatus(t, resp, diagCodeClientPath, diagWarn)
		if !strings.Contains(path.Message, "not checked") {
			t.Errorf("message = %q", path.Message)
		}
	})
}
