package db

import (
	"context"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/auth"
)

// The requester role must be storable through every repository setter, and
// the last admin guard must refuse a demotion to requester exactly as it
// refuses a demotion to user. Before the guard keyed on "anything but admin"
// it checked role == "user", so SetRole(lastAdmin, "requester") would have
// left the instance with no admin at all.

func TestSetRole_AcceptsRequester(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	if _, err := repo.Create(ctx, "root", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := repo.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := repo.Create(ctx, "reader", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetRole(ctx, u.ID, auth.RoleRequester); err != nil {
		t.Fatalf("SetRole requester: %v", err)
	}
	got, _ := repo.GetByID(ctx, u.ID)
	if got == nil || got.Role != auth.RoleRequester {
		t.Fatalf("role = %+v, want requester", got)
	}
	if err := repo.SetRoleUnguarded(ctx, u.ID, auth.RoleUser); err != nil {
		t.Fatalf("SetRoleUnguarded user: %v", err)
	}
	if err := repo.SetRoleUnguarded(ctx, u.ID, auth.RoleRequester); err != nil {
		t.Fatalf("SetRoleUnguarded requester: %v", err)
	}
}

func TestSetRole_LastAdminCannotBecomeRequester(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	admin, err := repo.Create(ctx, "root", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.PromoteFirstUser(ctx); err != nil {
		t.Fatal(err)
	}
	err = repo.SetRole(ctx, admin.ID, auth.RoleRequester)
	if err == nil || !strings.Contains(err.Error(), "last admin") {
		t.Fatalf("SetRole(last admin, requester) err = %v, want the last admin refusal", err)
	}
	got, _ := repo.GetByID(ctx, admin.ID)
	if got == nil || got.Role != auth.RoleAdmin {
		t.Fatalf("last admin role = %+v, want admin kept", got)
	}
}

func TestSetRole_RejectsUnknownRole(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)
	u, err := repo.Create(ctx, "x", "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"", "Requester", "superuser", "requester "} {
		if err := repo.SetRole(ctx, u.ID, role); err == nil {
			t.Errorf("SetRole(%q) accepted an invalid role", role)
		}
	}
}

func TestGetOrCreateByOIDC_ProvisionsRequester(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewUserRepo(database)

	u, err := repo.GetOrCreateByOIDC(ctx, "https://idp.example", "sub-r", "reader", "", "", auth.RoleRequester)
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != auth.RoleRequester {
		t.Fatalf("role = %q, want requester", u.Role)
	}
	bad, err := repo.GetOrCreateByOIDC(ctx, "https://idp.example", "sub-bad", "bad", "", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if bad.Role != auth.RoleUser {
		t.Fatalf("invalid role provisioned as %q, want user", bad.Role)
	}
}
