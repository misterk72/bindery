package main

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// stubAdoptionHandler records which adoption route ran.
type stubAdoptionHandler struct{ called []string }

func (h *stubAdoptionHandler) record(name string, w http.ResponseWriter) {
	h.called = append(h.called, name)
	w.WriteHeader(http.StatusNoContent)
}

func (h *stubAdoptionHandler) List(w http.ResponseWriter, _ *http.Request)    { h.record("list", w) }
func (h *stubAdoptionHandler) Summary(w http.ResponseWriter, _ *http.Request) { h.record("summary", w) }
func (h *stubAdoptionHandler) Adopt(w http.ResponseWriter, _ *http.Request)   { h.record("adopt", w) }
func (h *stubAdoptionHandler) Undo(w http.ResponseWriter, _ *http.Request)    { h.record("undo", w) }
func (h *stubAdoptionHandler) Ignore(w http.ResponseWriter, _ *http.Request)  { h.record("ignore", w) }
func (h *stubAdoptionHandler) Unignore(w http.ResponseWriter, _ *http.Request) {
	h.record("unignore", w)
}
func (h *stubAdoptionHandler) IgnoreMany(w http.ResponseWriter, _ *http.Request) {
	h.record("ignore-many", w)
}

// adoptionRoutes is every route registerAdoptionRoutes mounts, with a concrete
// path for each and the handler it must reach.
var adoptionRoutes = []struct {
	method, pattern, path, called string
}{
	{http.MethodGet, "/library/unmatched", "/library/unmatched", "list"},
	{http.MethodGet, "/library/unmatched/summary", "/library/unmatched/summary", "summary"},
	{http.MethodPost, "/library/unmatched/ignore", "/library/unmatched/ignore", "ignore-many"},
	{http.MethodPost, "/library/unmatched/{id}/adopt", "/library/unmatched/7/adopt", "adopt"},
	{http.MethodPost, "/library/unmatched/{id}/undo", "/library/unmatched/7/undo", "undo"},
	{http.MethodPost, "/library/unmatched/{id}/ignore", "/library/unmatched/7/ignore", "ignore"},
	{http.MethodPost, "/library/unmatched/{id}/unignore", "/library/unmatched/7/unignore", "unignore"},
}

// TestAdoptionRoutesEnumerated walks the mounted router so a route added to
// registerAdoptionRoutes without a row in adoptionRoutes (and so without the
// admin checks below) fails here.
func TestAdoptionRoutesEnumerated(t *testing.T) {
	router := chi.NewRouter()
	registerAdoptionRoutes(router, &stubAdoptionHandler{})
	var mounted, want []string
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted = append(mounted, method+" "+route)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, r := range adoptionRoutes {
		want = append(want, r.method+" "+r.pattern)
	}
	sort.Strings(mounted)
	sort.Strings(want)
	if strings.Join(mounted, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mounted adoption routes:\n%s\nwant:\n%s", strings.Join(mounted, "\n"), strings.Join(want, "\n"))
	}
}

// TestAdoptionRoutesRequireAdmin: the rows carry absolute library paths and
// adopt writes authors, books and book files, so a user role, or no role at
// all, is refused before any handler runs.
func TestAdoptionRoutesRequireAdmin(t *testing.T) {
	for _, role := range []string{"user", ""} {
		for _, rt := range adoptionRoutes {
			t.Run(role+" "+rt.method+" "+rt.path, func(t *testing.T) {
				h := &stubAdoptionHandler{}
				router := chi.NewRouter()
				registerAdoptionRoutes(router, h)
				req := httptest.NewRequest(rt.method, rt.path, nil)
				if role != "" {
					req = req.WithContext(auth.WithUserRole(req.Context(), role))
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden || len(h.called) != 0 {
					t.Fatalf("status = %d, called %v; want 403 and no handler", rec.Code, h.called)
				}
			})
		}
	}
}

// TestAdoptionRoutesAllowAdmin is the symmetry case.
func TestAdoptionRoutesAllowAdmin(t *testing.T) {
	for _, rt := range adoptionRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			h := &stubAdoptionHandler{}
			router := chi.NewRouter()
			registerAdoptionRoutes(router, h)
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req = req.WithContext(auth.WithUserRole(req.Context(), "admin"))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent || len(h.called) != 1 || h.called[0] != rt.called {
				t.Fatalf("status = %d, called %v; want 204 and %s", rec.Code, h.called, rt.called)
			}
		})
	}
}
