package calibre

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The contract test. It drives the real *PluginClient against the real
// calibre-bridge request handler, running as a Python subprocess with Calibre
// and Qt stubbed out (testdata/bridge_harness.py, which uses the same
// mechanism as the plugin's own conftest.py).
//
// Why it exists: every divergence in the review's protocol table, the
// User-Agent, the 400 semantics, the 503 backoff, the silently ignored
// coverPath, was found by reading two codebases side by side. Nothing in
// either repository's CI ran one against the other, so nothing caught them.
// Both sides have unit tests against their own fakes, and a fake is exactly
// what drifts.
//
// Why it lives in internal/calibre rather than a separate contract package:
// the subject is *PluginClient, and a useful contract test has to reach the
// backoff schedule and the capability TTL to keep the 503 row from taking
// thirty seconds. An out of package test would need those exported purely to
// be tested, which is a worse trade than one file with a build-time skip.
//
// Skips cleanly when python3 or the plugin checkout is absent, so the normal
// `go test ./...` on a machine with neither is unaffected. Point it at a
// checkout with BINDERY_PLUGIN_SRC=/path/to/bindery-plugins.
//
// Rows that need a capability the plugin under test does not advertise are
// skipped individually, and the test asserts the degradation instead. That is
// the compatibility rule stated as an executable check: run against
// calibre-bridge 0.5.0 it proves Bindery degrades, run against 0.6.0 it
// proves the new endpoints agree.

const contractPluginSrcEnv = "BINDERY_PLUGIN_SRC"

// bridge is one running harness process.
type bridge struct {
	url    string
	cancel func()
}

func pluginSourceRoot(t *testing.T) string {
	t.Helper()
	root := strings.TrimSpace(os.Getenv(contractPluginSrcEnv))
	if root == "" {
		// A sibling checkout is the layout the two repositories are normally
		// cloned in, so try it before giving up.
		wd, err := os.Getwd()
		if err != nil {
			t.Skipf("cannot resolve working directory: %v", err)
		}
		root = filepath.Join(wd, "..", "..", "..", "bindery-plugins")
	}
	handlers := filepath.Join(root, "plugins", "calibre-bridge", "plugin", "handlers.py")
	if _, err := os.Stat(handlers); err != nil {
		t.Skipf("plugin source not found at %s; set %s to a bindery-plugins checkout", handlers, contractPluginSrcEnv)
	}
	return filepath.Join(root, "plugins", "calibre-bridge")
}

func startBridge(t *testing.T, args ...string) *bridge {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not available: %v", err)
	}
	root := pluginSourceRoot(t)
	harness, err := filepath.Abs(filepath.Join("testdata", "bridge_harness.py"))
	if err != nil {
		t.Fatalf("resolve harness: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, python, append([]string{harness, root}, args...)...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Skipf("cannot start the harness: %v", err)
	}

	portCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if port, ok := strings.CutPrefix(scanner.Text(), "PORT "); ok {
				portCh <- strings.TrimSpace(port)
				return
			}
		}
		portCh <- ""
	}()

	var port string
	select {
	case port = <-portCh:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("harness did not report a port within 20s")
	}
	if port == "" {
		cancel()
		t.Fatal("harness exited without reporting a port")
	}
	if _, err := strconv.Atoi(port); err != nil {
		cancel()
		t.Fatalf("harness reported a bad port %q", port)
	}

	b := &bridge{url: "http://127.0.0.1:" + port, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	return b
}

// tempBook writes a file with an extension the plugin will accept as a format.
func tempBook(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("not really an epub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPluginContract(t *testing.T) {
	shrinkBackoff(t)
	const key = "contract-key"
	srv := startBridge(t, "--api-key", key, "--max-body", "4096", "--library", "/calibre-library")
	ctx := context.Background()
	c := NewPluginClient(srv.url, key)

	var caps map[string]bool
	t.Run("health advertises a capability list", func(t *testing.T) {
		h, err := c.fetchHealth(ctx)
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		if h.PluginVersion == "" || h.CalibreVersion == "" {
			t.Errorf("health = %+v, want both versions populated", h)
		}
		if h.Library != "/calibre-library" {
			t.Errorf("library = %q, want the active library path", h.Library)
		}
		caps = map[string]bool{}
		for _, name := range h.Capabilities {
			caps[name] = true
		}
		if !caps[pluginCapabilityBookMetadata] {
			t.Errorf("capabilities = %v, want at least %q", h.Capabilities, pluginCapabilityBookMetadata)
		}
		t.Logf("plugin %s advertises %v", h.PluginVersion, h.Capabilities)
	})

	t.Run("201 on a fresh book", func(t *testing.T) {
		id, err := c.Add(ctx, tempBook(t, "fresh.epub"), Metadata{
			Title:       "Dune",
			Authors:     []string{"Frank Herbert"},
			Identifiers: map[string]string{"bindery": "1001"},
		})
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if id <= 0 {
			t.Errorf("id = %d, want a positive Calibre id", id)
		}
	})

	t.Run("409 on a book the library already holds", func(t *testing.T) {
		meta := Metadata{Title: "Dune", Identifiers: map[string]string{"bindery": "2002"}}
		first, err := c.Add(ctx, tempBook(t, "dup.epub"), meta)
		if err != nil {
			t.Fatalf("first Add: %v", err)
		}
		second, err := c.Add(ctx, tempBook(t, "dup2.epub"), meta)
		if !errors.Is(err, ErrAlreadyInCalibre) {
			t.Fatalf("second Add error = %v, want ErrAlreadyInCalibre", err)
		}
		if second != first {
			t.Errorf("409 returned id %d, want the existing id %d", second, first)
		}
	})

	t.Run("401 on a bad token", func(t *testing.T) {
		bad := NewPluginClient(srv.url, "wrong")
		_, err := bad.Add(ctx, tempBook(t, "x.epub"), Metadata{Title: "Dune"})
		if err == nil || !strings.Contains(err.Error(), "authentication failed") {
			t.Fatalf("error = %v, want the authentication message", err)
		}
	})

	t.Run("400 without a code does not look like a metadata rejection", func(t *testing.T) {
		// A path the Calibre side cannot open is the #1346 mount mismatch.
		// Whether the plugin names it with a code or only in prose, the client
		// must not read it as "your metadata is bad" and re-send.
		_, err := c.Add(ctx, filepath.Join(t.TempDir(), "absent.epub"), Metadata{Title: "Dune"})
		if err == nil {
			t.Fatal("expected an error for a file the plugin cannot open")
		}
		if strings.Contains(err.Error(), "400") && looksLikeFileError(err.Error()) {
			return
		}
		t.Errorf("error = %v, want a 400 whose text reads as a file problem", err)
	})

	t.Run("400 on a path with no usable extension", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "no-extension")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := c.Add(ctx, path, Metadata{Title: "Dune"})
		if err == nil {
			t.Fatal("expected an error for an extensionless path")
		}
		if !looksLikeFileError(err.Error()) {
			t.Errorf("error = %v, want it classified as a file problem rather than a metadata one", err)
		}
	})

	t.Run("413 on an oversized body", func(t *testing.T) {
		huge := strings.Repeat("x", 8192)
		_, err := c.Add(ctx, tempBook(t, "big.epub"), Metadata{Title: "Dune", Description: huge})
		if err == nil {
			t.Fatal("expected an error for a body over the plugin's limit")
		}
		if !strings.Contains(err.Error(), "413") {
			t.Errorf("error = %v, want a 413", err)
		}
	})

	t.Run("cover capability", func(t *testing.T) {
		if !caps[pluginCapabilityCover] {
			// The degradation half of the contract: no capability, no field.
			if c.SupportsCover(ctx) {
				t.Error("SupportsCover = true against a plugin that does not advertise it")
			}
			t.Skipf("plugin does not advertise %q", pluginCapabilityCover)
		}
		cover := tempBook(t, "cover.jpg")
		if _, err := c.Add(ctx, tempBook(t, "withcover.epub"), Metadata{
			Title:       "Dune",
			CoverPath:   cover,
			Identifiers: map[string]string{"bindery": "3003"},
		}); err != nil {
			t.Fatalf("Add with a cover: %v", err)
		}
	})

	t.Run("path probe", func(t *testing.T) {
		if !caps[pluginCapabilityPathProbe] {
			if c.SupportsPathProbe(ctx) {
				t.Error("SupportsPathProbe = true against a plugin that does not advertise it")
			}
			t.Skipf("plugin does not advertise %q", pluginCapabilityPathProbe)
		}
		dir := t.TempDir()
		probe, err := c.ProbePath(ctx, dir)
		if err != nil {
			t.Fatalf("ProbePath: %v", err)
		}
		if !probe.Exists || !probe.IsDir || !probe.Readable {
			t.Errorf("probe of %s = %+v, want all true", dir, probe)
		}
		missing, err := c.ProbePath(ctx, filepath.Join(dir, "nope"))
		if err != nil {
			t.Fatalf("ProbePath on a missing path: %v", err)
		}
		if missing.Exists {
			t.Error("a missing path reported exists=true")
		}
	})

	t.Run("metadata update", func(t *testing.T) {
		if !caps[pluginCapabilityMetadataUpdate] {
			if c.SupportsMetadataUpdate(ctx) {
				t.Error("SupportsMetadataUpdate = true against a plugin that does not advertise it")
			}
			t.Skipf("plugin does not advertise %q", pluginCapabilityMetadataUpdate)
		}
		id, err := c.Add(ctx, tempBook(t, "patchable.epub"), Metadata{
			Title:       "Dune",
			Identifiers: map[string]string{"bindery": "4004"},
		})
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		fields, err := c.UpdateMetadata(ctx, id, Metadata{Title: "Dune", Series: "Dune Chronicles", SeriesIndex: "1"})
		if err != nil {
			t.Fatalf("UpdateMetadata: %v", err)
		}
		// An empty list here would mean the plugin accepted the request and
		// wrote nothing, which is what a body in the wrong shape looks like
		// from the outside. This assertion is the one that catches it.
		if len(fields) == 0 {
			t.Errorf("UpdateMetadata applied nothing; the request body shape does not match what the plugin reads")
		}
		if _, err := c.UpdateMetadata(ctx, 999999, Metadata{Title: "Dune"}); !errors.Is(err, ErrCalibreBookMissing) {
			t.Errorf("UpdateMetadata on a missing id = %v, want ErrCalibreBookMissing", err)
		}
	})

	t.Run("error codes", func(t *testing.T) {
		if !caps[pluginCapabilityErrorCodes] {
			t.Skipf("plugin does not advertise %q", pluginCapabilityErrorCodes)
		}
		_, err := c.Add(ctx, filepath.Join(t.TempDir(), "absent.epub"), Metadata{Title: "Dune"})
		if err == nil || !strings.Contains(err.Error(), "path_not_found") {
			t.Errorf("error = %v, want it to carry the path_not_found code", err)
		}
	})
}

// TestPluginContract_503Backoff drives the real handler's library-not-ready
// branch. protocol.md tells clients to retry with exponential backoff to about
// thirty seconds; the client used to give up after one flat two second wait.
func TestPluginContract_503Backoff(t *testing.T) {
	shrinkBackoff(t)
	srv := startBridge(t, "--unavailable", "3")
	c := NewPluginClient(srv.url, "")

	id, err := c.Add(context.Background(), tempBook(t, "slow.epub"), Metadata{
		Title:       "Dune",
		Identifiers: map[string]string{"bindery": "5005"},
	})
	if err != nil {
		t.Fatalf("Add across three 503s: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want a positive id once the library came back", id)
	}
}

// TestPluginContract_UserAgent pins the one header the plugin logs for
// support. protocol.md asks for "bindery/<semver> plugin-api/v1".
func TestPluginContract_UserAgent(t *testing.T) {
	if got := pluginUserAgent(); !strings.HasPrefix(got, "bindery/") || !strings.HasSuffix(got, " plugin-api/v1") {
		t.Fatalf("User-Agent = %q, want the shape protocol.md specifies", got)
	}
	if strings.Contains(pluginUserAgent(), "  ") {
		t.Errorf("User-Agent = %q, want a single space between the parts", pluginUserAgent())
	}

}
