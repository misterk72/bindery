package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// roleProvider is fakeProvider with a chosen stored role.
type roleProvider struct {
	*fakeProvider
	role string
}

func (p *roleProvider) UserRole(context.Context, int64) string { return p.role }

// serveLocal runs a request from 127.0.0.1 with a session cookie for user 42
// through Middleware then RestrictRequester, and reports the status, whether
// the handler ran, and the role and user it saw.
func serveLocal(t *testing.T, p Provider, path string) (int, bool, string, int64) {
	t.Helper()
	cookie, err := SignSession(testSecret32, 42, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var reached bool
	var role string
	var uid int64
	h := Middleware(p)(RestrictRequester(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached, role, uid = true, UserRoleFromContext(r.Context()), UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, reached, role, uid
}

// TestMiddleware_RequesterNotElevatedByMode: a requester's session keeps the
// requester role in local-only and disabled mode, so the guard refuses admin
// routes; the same session as role user is granted admin as before.
func TestMiddleware_RequesterNotElevatedByMode(t *testing.T) {
	for _, mode := range []Mode{ModeLocalOnly, ModeDisabled} {
		p := &roleProvider{fakeProvider: &fakeProvider{mode: mode, secret: testSecret32, operatorUserID: 1}, role: RoleRequester}
		code, reached, _, _ := serveLocal(t, p, "/api/v1/queue")
		if reached || code != http.StatusForbidden {
			t.Errorf("%s mode, requester session: status %d reached %v, want 403", mode, code, reached)
		}
		code, reached, role, _ := serveLocal(t, p, "/api/v1/requests")
		if !reached || role != RoleRequester {
			t.Errorf("%s mode, requester on an allowed route: status %d role %q, want the requester role", mode, code, role)
		}

		p.role = RoleUser
		if _, reached, role, _ := serveLocal(t, p, "/api/v1/queue"); !reached || role != RoleAdmin {
			t.Errorf("%s mode, user session: reached %v role %q, want the admin grant unchanged", mode, reached, role)
		}
	}
}

// TestMiddleware_UnreadableSessionNotElevatedByMode: when the role or the
// session epoch cannot be read, a valid cookie is not replaced by the mode's
// admin grant; it goes through as that user with no role and is held to the
// allow list.
func TestMiddleware_UnreadableSessionNotElevatedByMode(t *testing.T) {
	cases := []struct {
		name string
		p    Provider
	}{
		{"role lookup failed", &roleProvider{fakeProvider: &fakeProvider{mode: ModeLocalOnly, secret: testSecret32, operatorUserID: 1}, role: ""}},
		{"epoch lookup failed", &roleProvider{fakeProvider: &fakeProvider{mode: ModeLocalOnly, secret: testSecret32, operatorUserID: 1, epochErr: errors.New("database is locked")}, role: RoleUser}},
	}
	for _, c := range cases {
		code, reached, _, _ := serveLocal(t, c.p, "/api/v1/queue")
		if reached || code != http.StatusForbidden {
			t.Errorf("%s: GET /queue status %d reached %v, want 403", c.name, code, reached)
		}
		_, reached, role, uid := serveLocal(t, c.p, "/api/v1/requests")
		if !reached || role != "" || uid != 42 {
			t.Errorf("%s: allowed route reached %v role %q uid %d, want the session's user with no role", c.name, reached, role, uid)
		}
	}
}
