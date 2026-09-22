package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pluginStub(t *testing.T, caps string, probe func(path string) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"plugin_version":"0.6.0","calibre_version":"9.8","library":"/calibre-library","capabilities":[` + caps + `]}`))
		case "/v1/paths":
			if probe == nil {
				t.Errorf("unexpected path probe for %q", r.URL.Query().Get("path"))
				w.WriteHeader(http.StatusNotFound)
				return
			}
			status, body := probe(r.URL.Query().Get("path"))
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testConnection(t *testing.T, h *CalibreHandler) (int, map[string]string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/calibre/test", nil))
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// TestCalibre_Test_ReportsAnUnreachableLibraryRoot is review item 5. Test
// connection only called Health, so it reported "plugin reachable" while
// every push was about to fail with "No such file or directory" because the
// Calibre container mounts the library somewhere else (#1346).
func TestCalibre_Test_ReportsAnUnreachableLibraryRoot(t *testing.T) {
	h, repo, ctx := calibreFixture(t)
	root := t.TempDir()
	srv := pluginStub(t, `"book_metadata","path_probe"`, func(path string) (int, string) {
		if path != root {
			t.Errorf("probed %q, want %q", path, root)
		}
		return http.StatusOK, `{"path":"` + path + `","exists":false,"readable":false,"isDir":false}`
	})
	if err := repo.Set(ctx, SettingCalibreMode, "plugin"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibrePluginURL, srv.URL); err != nil {
		t.Fatal(err)
	}
	h = h.WithLibraryRoot(root)

	code, body := testConnection(t, h)
	if code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %v", code, body)
	}
	if !strings.Contains(body["error"], root) {
		t.Errorf("error = %q, want it to name the path", body["error"])
	}
	if !strings.Contains(body["error"], "push path remap") {
		t.Errorf("error = %q, want it to name the remedy", body["error"])
	}
}

// TestCalibre_Test_PassesWhenTheRootIsVisible keeps the happy path honest:
// the success message now says the path was checked, not just that the plugin
// answered.
func TestCalibre_Test_PassesWhenTheRootIsVisible(t *testing.T) {
	h, repo, ctx := calibreFixture(t)
	root := t.TempDir()
	srv := pluginStub(t, `"book_metadata","path_probe"`, func(path string) (int, string) {
		return http.StatusOK, `{"path":"` + path + `","exists":true,"readable":true,"isDir":true}`
	})
	if err := repo.Set(ctx, SettingCalibreMode, "plugin"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibrePluginURL, srv.URL); err != nil {
		t.Fatal(err)
	}
	h = h.WithLibraryRoot(root)

	code, body := testConnection(t, h)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", code, body)
	}
	if !strings.Contains(body["message"], root) {
		t.Errorf("message = %q, want it to name the path it checked", body["message"])
	}
}

// TestCalibre_Test_AppliesTheRemapBeforeProbing: a probe that skipped the
// remap would report on a path no push ever sends.
func TestCalibre_Test_AppliesTheRemapBeforeProbing(t *testing.T) {
	h, repo, ctx := calibreFixture(t)
	root := t.TempDir()
	var probed string
	srv := pluginStub(t, `"path_probe"`, func(path string) (int, string) {
		probed = path
		return http.StatusOK, `{"path":"` + path + `","exists":true,"readable":true,"isDir":true}`
	})
	for k, v := range map[string]string{
		SettingCalibreMode:          "plugin",
		SettingCalibrePluginURL:     srv.URL,
		SettingCalibrePushPathRemap: root + ":/mnt/books",
	} {
		if err := repo.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	h = h.WithLibraryRoot(root)

	if code, body := testConnection(t, h); code != http.StatusOK {
		t.Fatalf("status = %d: %v", code, body)
	}
	if probed != "/mnt/books" {
		t.Errorf("probed %q, want the remapped path", probed)
	}
}

// TestCalibre_Test_FallsBackWithoutTheCapability holds the compatibility rule:
// an older plugin gets exactly today's answer, with no probe attempted.
func TestCalibre_Test_FallsBackWithoutTheCapability(t *testing.T) {
	h, repo, ctx := calibreFixture(t)
	srv := pluginStub(t, `"book_metadata"`, nil)
	if err := repo.Set(ctx, SettingCalibreMode, "plugin"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, SettingCalibrePluginURL, srv.URL); err != nil {
		t.Fatal(err)
	}
	h = h.WithLibraryRoot(t.TempDir())

	code, body := testConnection(t, h)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", code, body)
	}
	if body["message"] != "plugin reachable" {
		t.Errorf("message = %q, want the pre-0.6.0 message", body["message"])
	}
}
