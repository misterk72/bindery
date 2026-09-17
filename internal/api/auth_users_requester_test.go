package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// The user management API accepts the requester role on create and on
// SetRole, and still refuses to demote the last admin to it.

func TestUserMgmt_Create_RequesterRole(t *testing.T) {
	h, users := newUserMgmtFixture(t)

	rec := httptest.NewRecorder()
	h.Create(rec, jsonReq(http.MethodPost, "/api/v1/auth/users",
		`{"username":"reader","password":"hunter2pass","role":"requester"}`, nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("Create status=%d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got userResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Role != auth.RoleRequester {
		t.Errorf("response role=%q, want requester", got.Role)
	}
	u, _ := users.GetByUsername(context.Background(), "reader")
	if u == nil || u.Role != auth.RoleRequester {
		t.Fatalf("requester role not persisted, got %+v", u)
	}
}

func TestUserMgmt_SetRole_Requester(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	u, _ := users.Create(ctx, "reader", "h")

	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"requester"}`, u.ID, ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("SetRole status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := users.GetByID(ctx, u.ID)
	if got == nil || got.Role != auth.RoleRequester {
		t.Fatalf("role = %+v, want requester", got)
	}
}

func TestUserMgmt_SetRole_DemoteLastAdminToRequester(t *testing.T) {
	h, users := newUserMgmtFixture(t)
	ctx := context.Background()
	admin, _ := users.Create(ctx, "onlyadmin", "h")
	_ = users.SetRole(ctx, admin.ID, auth.RoleAdmin)

	rec := httptest.NewRecorder()
	h.SetRole(rec, jsonReqWithID(http.MethodPut, "/api/v1/auth/users/x/role", `{"role":"requester"}`, admin.ID, ctx))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("demote last admin to requester status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := users.GetByID(ctx, admin.ID)
	if got == nil || got.Role != auth.RoleAdmin {
		t.Fatalf("last admin was demoted to requester despite the guard, got %+v", got)
	}
}
