package api

import (
	"net/http"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// OIDC and the requester role (security review item S5). With
// BINDERY_OIDC_ADMIN_GROUP set, the group claim decides admin and nothing
// else:
//
//   - in the admin group gives admin;
//   - not in the group and currently admin gives the configured default role
//     (user when that default is admin);
//   - otherwise the current role is kept.
//
// Before this change a login outside the admin group always set "user", so a
// requester became a full user the next time they signed in.

// seedOIDCUser provisions the fake IdP's subject with role and returns its id.
func seedOIDCUser(t *testing.T, h *OIDCHandler, idp *fakeIDP, sub, username, role string) int64 {
	t.Helper()
	u, err := h.users.GetOrCreateByOIDC(t.Context(), idp.server.URL, sub, username, "", "", role)
	if err != nil {
		t.Fatalf("seed %s: %v", role, err)
	}
	if u.Role != role {
		t.Fatalf("seeded role = %q, want %q", u.Role, role)
	}
	return u.ID
}

func roleAfterLogin(t *testing.T, h *OIDCHandler, id int64) string {
	t.Helper()
	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("callback status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := h.users.GetByID(t.Context(), id)
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	return u.Role
}

// TestCallback_GroupClaim_RequesterOutsideGroupStaysRequester is the fail
// before case: on the base branch the requester comes back as "user".
func TestCallback_GroupClaim_RequesterOutsideGroupStaysRequester(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-req", "nonce": "test-nonce", "preferred_username": "gr",
		"groups": []string{"readers"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin")
	id := seedOIDCUser(t, h, idp, "g-req", "gr", auth.RoleRequester)

	if got := roleAfterLogin(t, h, id); got != auth.RoleRequester {
		t.Fatalf("Role=%q, want requester (outside the admin group, not an admin, so unchanged)", got)
	}
}

func TestCallback_GroupClaim_UserOutsideGroupStaysUser(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-user", "nonce": "test-nonce", "preferred_username": "gu",
		"groups": []string{"readers"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin").WithOIDCDefaultRole(auth.RoleRequester)
	id := seedOIDCUser(t, h, idp, "g-user", "gu", auth.RoleUser)

	if got := roleAfterLogin(t, h, id); got != auth.RoleUser {
		t.Fatalf("Role=%q, want user (a non admin keeps the role an admin gave them)", got)
	}
}

func TestCallback_GroupClaim_RequesterInGroupBecomesAdmin(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-req-admin", "nonce": "test-nonce", "preferred_username": "gra",
		"groups": []string{"bindery-admin"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin")
	id := seedOIDCUser(t, h, idp, "g-req-admin", "gra", auth.RoleRequester)

	if got := roleAfterLogin(t, h, id); got != auth.RoleAdmin {
		t.Fatalf("Role=%q, want admin (in the admin group)", got)
	}
}

// TestCallback_GroupClaim_AdminLeavingGroupGetsDefaultRole keeps the demotion
// half of the rule: an admin removed from the group loses admin, landing on
// the configured default.
func TestCallback_GroupClaim_AdminLeavingGroupGetsDefaultRole(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-admin-out", "nonce": "test-nonce", "preferred_username": "gao",
		"groups": []string{"readers"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin").WithOIDCDefaultRole(auth.RoleRequester)
	id := seedOIDCUser(t, h, idp, "g-admin-out", "gao", auth.RoleAdmin)

	if got := roleAfterLogin(t, h, id); got != auth.RoleRequester {
		t.Fatalf("Role=%q, want requester (admin left the group, default role is requester)", got)
	}
}

func TestCallback_GroupClaim_AdminLeavingGroupWithAdminDefaultGetsUser(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "g-admin-def", "nonce": "test-nonce", "preferred_username": "gad",
		"groups": []string{"readers"},
	}
	h, _, _ := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCAdminGroup("bindery-admin").WithOIDCDefaultRole(auth.RoleAdmin)
	id := seedOIDCUser(t, h, idp, "g-admin-def", "gad", auth.RoleAdmin)

	if got := roleAfterLogin(t, h, id); got != auth.RoleUser {
		t.Fatalf("Role=%q, want user (leaving the admin group must always demote)", got)
	}
}

// TestCallback_DefaultRole_Requester: BINDERY_OIDC_DEFAULT_ROLE=requester
// provisions new logins as requesters.
func TestCallback_DefaultRole_Requester(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = map[string]any{
		"sub": "u-requester", "nonce": "test-nonce", "preferred_username": "reader",
	}
	h, users, ctx := newCallbackTestHandler(t, idp, nil, false)
	h.WithOIDCDefaultRole(auth.RoleRequester)

	if rec := doCallback(t, h); rec.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	u, err := users.GetByOIDC(ctx, idp.server.URL, "u-requester")
	if err != nil || u == nil {
		t.Fatalf("lookup: u=%v err=%v", u, err)
	}
	if u.Role != auth.RoleRequester {
		t.Fatalf("Role=%q, want requester", u.Role)
	}
}

func TestGroupSyncRole(t *testing.T) {
	cases := []struct {
		current, def string
		inGroup      bool
		want         string
	}{
		{auth.RoleRequester, auth.RoleUser, true, auth.RoleAdmin},
		{auth.RoleUser, auth.RoleUser, true, auth.RoleAdmin},
		{auth.RoleAdmin, auth.RoleUser, true, auth.RoleAdmin},
		{auth.RoleAdmin, auth.RoleUser, false, auth.RoleUser},
		{auth.RoleAdmin, auth.RoleRequester, false, auth.RoleRequester},
		{auth.RoleAdmin, auth.RoleAdmin, false, auth.RoleUser},
		{auth.RoleAdmin, "bogus", false, auth.RoleUser},
		{auth.RoleUser, auth.RoleRequester, false, auth.RoleUser},
		{auth.RoleRequester, auth.RoleUser, false, auth.RoleRequester},
	}
	for _, c := range cases {
		if got := groupSyncRole(c.current, c.inGroup, c.def); got != c.want {
			t.Errorf("groupSyncRole(%q, inGroup=%v, default=%q) = %q, want %q", c.current, c.inGroup, c.def, got, c.want)
		}
	}
}
