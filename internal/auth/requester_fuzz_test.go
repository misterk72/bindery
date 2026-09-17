package auth

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// FuzzRequesterAllowList drives raw request lines through a chi router shaped
// like the real one (an /api tree and an /api/v1 tree, each behind the guard,
// plus the SPA catch all) and checks one property: no request line, however
// spelled, reaches an API handler whose method and pattern are not on
// RequesterAllowList. Request lines are parsed by net/http's own reader, so the
// fuzzer explores what the server would actually see, and every route in
// requesterDeniedRoutes is registered as a live target.
func FuzzRequesterAllowList(f *testing.F) {
	for _, d := range requesterDeniedRoutes {
		for _, v := range variants(d.method, d.path) {
			f.Add(v.method, v.target)
		}
	}
	for _, rt := range RequesterAllowList {
		f.Add(rt.Method, concretePath(rt.Pattern))
	}
	for _, s := range []string{
		"/api/v1/requests/..%2fqueue%2fgrab", "/api/v1/requests/%2e%2e/queue/grab",
		"/api/v1/requests;/../queue/grab", "//api/v1/queue/grab", "/api/v1/requests/7?/../../queue",
		"http://host/api/v1/queue/grab", "*", "/api/v1/requests/library/../../queue/grab",
		"/api/v1/requests/\x00/grab", "/api/v1/images#/../queue",
	} {
		f.Add(http.MethodPost, s)
		f.Add(http.MethodGet, s)
	}

	allowed := make(map[string]bool, len(RequesterAllowList))
	for _, rt := range RequesterAllowList {
		allowed[rt.Method+" "+rt.Pattern] = true
	}

	var reachedPattern, reachedMethod string
	record := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reachedPattern = chi.RouteContext(r.Context()).RoutePattern()
		reachedMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	})
	guard := restrictRequester(defaultRequesterMatcher, NewRequesterLimiter(1<<30, 1<<30, 0, 4), NewRequesterLimiter(1<<30, 1<<30, 0, 4))
	asRequester := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithUserRole(WithUserID(r.Context(), 42), RoleRequester)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	router := chi.NewRouter()
	router.Route("/api", func(r chi.Router) {
		r.Use(asRequester, guard)
		r.Get("/queue", record)
	})
	router.Route("/api/v1", func(r chi.Router) {
		r.Use(asRequester, guard)
		seen := map[string]bool{}
		register := func(method, pattern string) {
			if seen[method+" "+pattern] {
				return
			}
			seen[method+" "+pattern] = true
			r.Method(method, strings.TrimPrefix(pattern, "/api/v1"), record)
		}
		for _, rt := range RequesterAllowList {
			register(rt.Method, rt.Pattern)
		}
		for _, d := range requesterDeniedRoutes {
			if strings.HasPrefix(d.path, "/api/v1/") {
				register(d.method, d.path)
			}
		}
		for _, p := range []string{"/api/v1/book/{id}/file", "/api/v1/queue/{id}", "/api/v1/author/{id}", "/api/v1/requests/{id}/approve"} {
			register(http.MethodGet, p)
			register(http.MethodPost, p)
			register(http.MethodDelete, p)
		}
	})
	router.Get("/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	f.Fuzz(func(t *testing.T, method, target string) {
		line := method + " " + target + " HTTP/1.1\r\nHost: bindery\r\n\r\n"
		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(line)))
		if err != nil {
			return
		}
		req.RemoteAddr = "192.0.2.1:1234"
		reachedPattern, reachedMethod = "", ""
		router.ServeHTTP(httptest.NewRecorder(), req)
		if reachedPattern == "" {
			return
		}
		m := reachedMethod
		if m == http.MethodHead {
			m = http.MethodGet
		}
		if !allowed[m+" "+reachedPattern] {
			t.Fatalf("request line %q reached %s %s, which is not on the requester allow list", method+" "+target, reachedMethod, reachedPattern)
		}
	})
}
