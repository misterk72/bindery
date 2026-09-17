package auth

import (
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// RequesterRoute is one method and chi pattern a requester may call. Patterns
// are full paths as the API routers see them, after BINDERY_URL_BASE has been
// stripped by http.StripPrefix in main.go.
type RequesterRoute struct {
	Method  string
	Pattern string
	// Limited routes spend metadata provider quota, so a requester's calls to
	// them go through the per user limiter (see requesterLimiter).
	Limited bool
}

// RequesterAllowList is every API route a requester may call. It is the whole
// of the requester's API surface: RestrictRequester answers 403 for any
// method and path not listed here, including routes added to the router after
// this list was written. That default is the point. The router is built
// inline in main() and cannot be enumerated in a test, so a deny list would
// silently open every new route to requesters; an allow list closes it.
//
// GET and HEAD only, apart from the auth posts a signed in person needs and
// the requester's own /requests routes. Deliberately absent: /author, /book
// and /series reads (the library is browsed through the /requests/library
// projection, which carries no file paths), /book/{id}/file, /queue, /system,
// /setting, and everything else.
var RequesterAllowList = []RequesterRoute{
	{Method: http.MethodGet, Pattern: "/api/v1/health"},

	// Session plumbing. login, logout, status, csrf and the OIDC login and
	// callback are also in AllowUnauthPath; they are listed here so a
	// requester who is already signed in can switch accounts.
	{Method: http.MethodGet, Pattern: "/api/v1/auth/status"},
	{Method: http.MethodGet, Pattern: "/api/v1/auth/csrf"},
	{Method: http.MethodGet, Pattern: "/api/v1/auth/config"},
	{Method: http.MethodGet, Pattern: "/api/v1/auth/oidc/providers"},
	{Method: http.MethodGet, Pattern: "/api/v1/auth/oidc/{provider}/login"},
	{Method: http.MethodGet, Pattern: "/api/v1/auth/oidc/{provider}/callback"},
	{Method: http.MethodPost, Pattern: "/api/v1/auth/login"},
	{Method: http.MethodPost, Pattern: "/api/v1/auth/logout"},
	{Method: http.MethodPost, Pattern: "/api/v1/auth/password"},

	// The metadata searches the Add dialog runs. Each call spends provider
	// quota, hence Limited.
	{Method: http.MethodGet, Pattern: "/api/v1/search/author", Limited: true},
	{Method: http.MethodGet, Pattern: "/api/v1/search/book", Limited: true},
	{Method: http.MethodGet, Pattern: "/api/v1/book/lookup", Limited: true},

	// Cover images, through the SSRF guarded proxy.
	{Method: http.MethodGet, Pattern: "/api/v1/images"},

	// The requester's own requests and the read only library projection.
	{Method: http.MethodGet, Pattern: "/api/v1/requests"},
	{Method: http.MethodPost, Pattern: "/api/v1/requests"},
	{Method: http.MethodDelete, Pattern: "/api/v1/requests/{id}"},
	{Method: http.MethodGet, Pattern: "/api/v1/requests/library"},
}

// requesterMatcher is built once, at package init, from RequesterAllowList
// (plan item P6) and shared by every request.
type requesterMatcher struct {
	mux     *chi.Mux
	limited map[string]bool // "METHOD pattern" -> Limited
}

func newRequesterMatcher(routes []RequesterRoute) *requesterMatcher {
	m := &requesterMatcher{mux: chi.NewRouter(), limited: make(map[string]bool, len(routes))}
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, rt := range routes {
		m.mux.Method(rt.Method, rt.Pattern, noop)
		m.limited[rt.Method+" "+rt.Pattern] = rt.Limited
	}
	return m
}

var defaultRequesterMatcher = newRequesterMatcher(RequesterAllowList)

// match reports whether method and the request's path are on the allow list,
// and whether that route is rate limited. It refuses, before any lookup, every
// path whose routing could differ from its plain reading:
//
//   - r.URL.RawPath set: the path carried an escape whose decoded form is not
//     its canonical encoding (an encoded slash, an escaped letter). chi routes
//     on RawPath when it is set, so the decoded Path is not what chi sees.
//   - a path that path.Clean would change: dot segments, doubled slashes and a
//     trailing slash.
//   - a percent sign, backslash or control character left in the decoded path.
//
// HEAD is matched as GET, the way net/http treats it. The method is r.Method
// only; X-HTTP-Method-Override and similar headers are never consulted, and a
// chi routing method that disagrees with r.Method is refused.
func (m *requesterMatcher) match(r *http.Request) (allowed, limited bool) {
	method := r.Method
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RouteMethod != "" && rctx.RouteMethod != method {
		return false, false
	}
	if method == http.MethodHead {
		method = http.MethodGet
	}
	if r.URL == nil || r.URL.RawPath != "" {
		return false, false
	}
	p := r.URL.Path
	if !canonicalRequesterPath(p) {
		return false, false
	}
	pattern := m.mux.Find(chi.NewRouteContext(), method, p)
	if pattern == "" {
		return false, false
	}
	return true, m.limited[method+" "+pattern]
}

func canonicalRequesterPath(p string) bool {
	if p == "" || p[0] != '/' || path.Clean(p) != p {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '%' || c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// restrictedRole reports whether a request carrying role and userID is held to
// the requester allow list. admin and user are not. A request with no user and
// no role reached here only through AllowUnauthPath (Middleware stamps the
// admin role on every other anonymous grant), so it is left to Middleware's
// own rules. Every other combination is restricted: the requester role, and a
// signed in user whose role could not be read, which fails closed.
func restrictedRole(role string, userID int64) bool {
	switch role {
	case RoleAdmin, RoleUser:
		return false
	case "":
		return userID != 0
	default:
		return true
	}
}

// RestrictRequester holds requesters to RequesterAllowList. It must run after
// Middleware, which resolves the role, and is installed right after it in
// useAPIAuth (cmd/bindery/sensitive_routes.go), so it covers both the /api/v1
// tree and the Arr compatible /api tree.
//
// Admin and user requests pass untouched. So do requests the auth mode admits
// as the install (API key, disabled mode, a trusted local client in
// local-only mode): Middleware stamps those admin, even when a requester's
// cookie rides along. The requester role therefore only restricts anything
// when the auth mode is enabled or proxy.
func RestrictRequester(next http.Handler) http.Handler {
	return restrictRequester(defaultRequesterMatcher, defaultRequesterLimiter)(next)
}

func restrictRequester(m *requesterMatcher, limiter *requesterLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			uid := UserIDFromContext(ctx)
			if !restrictedRole(UserRoleFromContext(ctx), uid) {
				next.ServeHTTP(w, r)
				return
			}
			allowed, limited := m.match(r)
			if !allowed {
				// The path is caller controlled; log only its length and the
				// method so a probing requester cannot write into the log.
				slog.Debug("requester guard: route not on the allow list",
					"user_id", uid, "method", sanitizeMethod(r.Method), "path_len", len(r.URL.Path))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"not available to requesters"}`))
				return
			}
			if limited && limiter != nil {
				if ok, retry := limiter.allow(uid); !ok {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", strconv.Itoa(retry))
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":"too many searches, try again shortly"}`))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func sanitizeMethod(m string) string {
	if len(m) > 10 {
		m = m[:10]
	}
	return strings.Map(func(r rune) rune {
		if r < 'A' || r > 'Z' {
			return '?'
		}
		return r
	}, m)
}
