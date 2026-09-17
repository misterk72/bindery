package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// These tests put the requester allow list behind the real auth stack
// (useAPIAuth over the DB backed provider), the real BINDERY_URL_BASE
// handling (mountUnderURLBase) and both API trees, the way main() builds them.

// universalStub satisfies every register* route handler interface in this
// package, so chi.Walk can enumerate every route those helpers mount.
type universalStub struct{}

func (universalStub) h(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func (s universalStub) List(w http.ResponseWriter, _ *http.Request)                { s.h(w) }
func (s universalStub) Get(w http.ResponseWriter, _ *http.Request)                 { s.h(w) }
func (s universalStub) Create(w http.ResponseWriter, _ *http.Request)              { s.h(w) }
func (s universalStub) Update(w http.ResponseWriter, _ *http.Request)              { s.h(w) }
func (s universalStub) Delete(w http.ResponseWriter, _ *http.Request)              { s.h(w) }
func (s universalStub) Test(w http.ResponseWriter, _ *http.Request)                { s.h(w) }
func (s universalStub) TestConfig(w http.ResponseWriter, _ *http.Request)          { s.h(w) }
func (s universalStub) Sync(w http.ResponseWriter, _ *http.Request)                { s.h(w) }
func (s universalStub) SearchQuery(w http.ResponseWriter, _ *http.Request)         { s.h(w) }
func (s universalStub) LastSearchDebug(w http.ResponseWriter, _ *http.Request)     { s.h(w) }
func (s universalStub) ImportCSV(w http.ResponseWriter, _ *http.Request)           { s.h(w) }
func (s universalStub) ImportReadarr(w http.ResponseWriter, _ *http.Request)       { s.h(w) }
func (s universalStub) ImportReadarrStatus(w http.ResponseWriter, _ *http.Request) { s.h(w) }
func (s universalStub) ImportGoodreadsPreview(w http.ResponseWriter, _ *http.Request) {
	s.h(w)
}
func (s universalStub) ImportGoodreadsCommit(w http.ResponseWriter, _ *http.Request) { s.h(w) }
func (s universalStub) Export(w http.ResponseWriter, _ *http.Request)                { s.h(w) }
func (s universalStub) GetLevel(w http.ResponseWriter, _ *http.Request)              { s.h(w) }
func (s universalStub) SetLevel(w http.ResponseWriter, _ *http.Request)              { s.h(w) }
func (s universalStub) TestDiscovery(w http.ResponseWriter, _ *http.Request)         { s.h(w) }
func (s universalStub) ScanStatus(w http.ResponseWriter, _ *http.Request)            { s.h(w) }
func (s universalStub) GetConfig(w http.ResponseWriter, _ *http.Request)             { s.h(w) }
func (s universalStub) SetConfig(w http.ResponseWriter, _ *http.Request)             { s.h(w) }
func (s universalStub) Start(w http.ResponseWriter, _ *http.Request)                 { s.h(w) }
func (s universalStub) Status(w http.ResponseWriter, _ *http.Request)                { s.h(w) }
func (s universalStub) SearchHardcover(w http.ResponseWriter, _ *http.Request)       { s.h(w) }
func (s universalStub) Monitor(w http.ResponseWriter, _ *http.Request)               { s.h(w) }
func (s universalStub) AddBook(w http.ResponseWriter, _ *http.Request)               { s.h(w) }
func (s universalStub) RemoveBook(w http.ResponseWriter, _ *http.Request)            { s.h(w) }
func (s universalStub) SetPrimaryBook(w http.ResponseWriter, _ *http.Request)        { s.h(w) }
func (s universalStub) Fill(w http.ResponseWriter, _ *http.Request)                  { s.h(w) }
func (s universalStub) ApplyGenres(w http.ResponseWriter, _ *http.Request)           { s.h(w) }
func (s universalStub) ClearGenres(w http.ResponseWriter, _ *http.Request)           { s.h(w) }
func (s universalStub) GetHardcoverLink(w http.ResponseWriter, _ *http.Request)      { s.h(w) }
func (s universalStub) AutoLinkHardcover(w http.ResponseWriter, _ *http.Request)     { s.h(w) }
func (s universalStub) PutHardcoverLink(w http.ResponseWriter, _ *http.Request)      { s.h(w) }
func (s universalStub) DeleteHardcoverLink(w http.ResponseWriter, _ *http.Request)   { s.h(w) }
func (s universalStub) HardcoverDiff(w http.ResponseWriter, _ *http.Request)         { s.h(w) }

// registerEnumerableRoutes mounts every register* helper main() uses.
func registerEnumerableRoutes(r chi.Router) {
	s := universalStub{}
	registerIndexerRoutes(r, s)
	registerProwlarrRoutes(r, s)
	registerRootFolderRoutes(r, s)
	registerDownloadClientRoutes(r, s)
	registerOIDCDiscoveryRoutes(r, s)
	registerSystemLogRoutes(r, s)
	registerStorageRoutes(r, s)
	registerLibraryScanStatusRoute(r, s)
	registerSeriesRoutes(r, s)
	registerGrimmoryRoutes(r, s)
	registerGrimmorySyncRoutes(r, s)
	registerCalibreIntegrationRoutes(r, s, s, s)
	registerMigrateRoutes(r, s)
}

type requesterFixture struct {
	handler    http.Handler
	cookie     *http.Cookie
	userCookie *http.Cookie
	settings   *db.SettingsRepo
	users      *db.UserRepo
	provider   *dbAuthProvider
	requester  *db.User
}

// newRequesterFixture builds the server shape: both API trees behind
// useAPIAuth, a stand in for a handful of main()'s inline routes, every
// enumerable register* helper, and the whole thing under urlBase.
func newRequesterFixture(t *testing.T, urlBase string) *requesterFixture {
	t.Helper()
	conn, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	settings := db.NewSettingsRepo(conn)
	users := db.NewUserRepo(conn)
	secret := strings.Repeat("s", 32)
	if err := settings.Set(ctx, api.SettingAuthSessionSecret, secret); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, api.SettingAuthMode, string(auth.ModeEnabled)); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(ctx, "admin", "x"); err != nil {
		t.Fatal(err)
	}
	if err := users.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := users.Create(ctx, "reader", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := users.SetRole(ctx, req.ID, auth.RoleRequester); err != nil {
		t.Fatal(err)
	}
	plain, err := users.Create(ctx, "plain", "x")
	if err != nil {
		t.Fatal(err)
	}
	provider := &dbAuthProvider{settings: settings, users: users}

	sign := func(id int64) *http.Cookie {
		v, err := auth.SignSessionWithEpoch([]byte(secret), id, 1, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: auth.SessionCookieName, Value: v}
	}

	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		useAPIAuth(r, provider)
		r.Get("/queue", ok)
	})
	r.Route("/api/v1", func(r chi.Router) {
		useAPIAuth(r, provider)
		r.Get("/health", ok)
		r.Get("/auth/status", ok)
		r.Get("/search/book", ok)
		r.Get("/book/lookup", ok)
		r.Get("/images", ok)
		r.Post("/queue/grab", ok)
		r.Get("/queue", ok)
		r.Get("/book/{id}", ok)
		r.Get("/book/{id}/file", ok)
		r.Delete("/book/{id}", ok)
		r.Post("/author", ok)
		r.Post("/author/book", ok)
		r.Get("/setting", ok)
		r.Post("/library/scan", ok)
		registerEnumerableRoutes(r)
	})
	r.Get("/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	return &requesterFixture{
		handler:    mountUnderURLBase(r, urlBase),
		cookie:     sign(req.ID),
		userCookie: sign(plain.ID),
		settings:   settings,
		users:      users,
		provider:   provider,
		requester:  req,
	}
}

func (f *requesterFixture) do(method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "203.0.113.9:4000" // not local, so local-only cannot grant admin
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Requested-With", "bindery-ui")
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func TestRequesterGuard_BothAPITreesAndURLBase(t *testing.T) {
	for _, base := range []string{"", "/bindery"} {
		f := newRequesterFixture(t, base)
		denied := []struct{ method, path string }{
			{http.MethodPost, "/api/v1/queue/grab"},
			{http.MethodGet, "/api/v1/queue"},
			{http.MethodGet, "/api/queue"},
			{http.MethodGet, "/api/v1/book/7"},
			{http.MethodGet, "/api/v1/book/7/file"},
			{http.MethodDelete, "/api/v1/book/7"},
			{http.MethodPost, "/api/v1/author"},
			{http.MethodPost, "/api/v1/author/book"},
			{http.MethodGet, "/api/v1/setting"},
			{http.MethodPost, "/api/v1/library/scan"},
			{http.MethodGet, "/api/v1/rootfolder"},
			{http.MethodGet, "/api/v1/series"},
			{http.MethodGet, "/api/v1/indexer/search"},
			{http.MethodGet, "/api/v1/grimmory/config"},
			{http.MethodGet, "/api/v1/book/7%2Ffile"},
			{http.MethodGet, "/api/v1//queue"},
			{http.MethodGet, "/api/v1/queue/"},
			{http.MethodGet, "/api/v1/./queue"},
		}
		for _, d := range denied {
			rec := f.do(d.method, base+d.path, f.cookie)
			if rec.Code != http.StatusForbidden {
				t.Errorf("base %q requester %s %s: status %d, want 403 (body %s)", base, d.method, d.path, rec.Code, rec.Body.String())
			}
		}
		for _, a := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/health"},
			{http.MethodGet, "/api/v1/auth/status"},
			{http.MethodGet, "/api/v1/search/book?term=dune"},
			{http.MethodGet, "/api/v1/book/lookup?isbn=9780441013593"},
			{http.MethodGet, "/api/v1/images?url=https://covers.example/x.jpg"},
		} {
			if rec := f.do(a.method, base+a.path, f.cookie); rec.Code != http.StatusNoContent {
				t.Errorf("base %q requester %s %s: status %d, want the handler's 204", base, a.method, a.path, rec.Code)
			}
		}
		// Role user is untouched by the guard.
		for _, p := range []string{"/api/v1/queue", "/api/queue", "/api/v1/book/7/file", "/api/v1/series"} {
			if rec := f.do(http.MethodGet, base+p, f.userCookie); rec.Code != http.StatusNoContent {
				t.Errorf("base %q role user GET %s: status %d, want 204", base, p, rec.Code)
			}
		}
	}
}

// TestRequesterGuard_EnumerableRoutes walks every route the register* helpers
// mount and asserts a requester gets 403 on each one that is not on the allow
// list. The inline routes in main() cannot be walked; the allow list's
// default deny covers them, and TestRestrictRequester_DeniesEveryListedRoute
// names them one by one.
func TestRequesterGuard_EnumerableRoutes(t *testing.T) {
	f := newRequesterFixture(t, "")
	walker := chi.NewRouter()
	registerEnumerableRoutes(walker)
	allowed := map[string]bool{}
	for _, rt := range auth.RequesterAllowList {
		allowed[rt.Method+" "+rt.Pattern] = true
	}
	n := 0
	err := chi.Walk(walker, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		full := "/api/v1" + route
		if allowed[method+" "+full] {
			return nil
		}
		n++
		path := strings.NewReplacer("{id}", "7", "{bookId}", "8", "{runID}", "9").Replace(full)
		if rec := f.do(method, path, f.cookie); rec.Code != http.StatusForbidden {
			t.Errorf("requester %s %s: status %d, want 403", method, path, rec.Code)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 40 {
		t.Fatalf("walked %d routes, expected the register* helpers to mount more", n)
	}
}

// TestRequesterGuard_AdminGrantingModesUnaffected: disabled mode and the API
// key admit the install as admin, and a requester cookie riding along does
// not change that. The requester role restricts only under enabled or proxy.
func TestRequesterGuard_AdminGrantingModesUnaffected(t *testing.T) {
	f := newRequesterFixture(t, "")
	ctx := context.Background()
	if err := f.settings.Set(ctx, api.SettingAuthMode, string(auth.ModeDisabled)); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodGet, "/api/v1/queue", f.cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("disabled mode with a requester cookie: status %d, want 204", rec.Code)
	}
	if err := f.settings.Set(ctx, api.SettingAuthMode, string(auth.ModeEnabled)); err != nil {
		t.Fatal(err)
	}
	if err := f.settings.Set(ctx, api.SettingAuthAPIKey, "k-requester-test"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	req.AddCookie(f.cookie)
	req.Header.Set("X-Api-Key", "k-requester-test")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("API key with a requester cookie: status %d, want 204", rec.Code)
	}
}

// TestRequesterGuard_DemotionTakesEffectAtOnce: the role is read per request,
// so a user demoted to requester is restricted on their next call with the
// same cookie.
func TestRequesterGuard_DemotionTakesEffectAtOnce(t *testing.T) {
	f := newRequesterFixture(t, "")
	if rec := f.do(http.MethodGet, "/api/v1/queue", f.userCookie); rec.Code != http.StatusNoContent {
		t.Fatalf("before demotion: %d", rec.Code)
	}
	plain, _ := f.users.GetByUsername(context.Background(), "plain")
	if err := f.users.SetRole(context.Background(), plain.ID, auth.RoleRequester); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodGet, "/api/v1/queue", f.userCookie); rec.Code != http.StatusForbidden {
		t.Fatalf("after demotion: status %d, want 403", rec.Code)
	}
}

// TestRequesterGuard_OPDSDenied: the OPDS tree serves book files, so a
// requester is refused there by cookie and by Basic credentials alike.
func TestRequesterGuard_OPDSDenied(t *testing.T) {
	f := newRequesterFixture(t, "")
	ctx := context.Background()
	hash, err := auth.HashPassword("requester-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.users.UpdatePassword(ctx, f.requester.ID, hash); err != nil {
		t.Fatal(err)
	}
	// UpdatePassword bumps the session epoch; re-sign the cookie.
	epoch, err := f.users.GetSessionEpoch(ctx, f.requester.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret := f.provider.SessionSecret()
	v, err := auth.SignSessionWithEpoch(secret, f.requester.ID, epoch, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.SessionCookieName, Value: v}

	opds := chi.NewRouter()
	opds.Route("/opds", func(r chi.Router) {
		r.Use(api.OPDSAuth(f.provider, f.users, auth.NewLoginLimiter(50, time.Minute)))
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		r.Get("/book/{id}/file", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})
	for _, p := range []string{"/opds/", "/opds/book/7/file"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		opds.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("requester cookie GET %s: status %d, want 403", p, rec.Code)
		}

		req = httptest.NewRequest(http.MethodGet, p, nil)
		req.RemoteAddr = "203.0.113.9:4000"
		req.SetBasicAuth("reader", "requester-password")
		rec = httptest.NewRecorder()
		opds.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("requester Basic GET %s: status %d, want 403", p, rec.Code)
		}
	}
}
