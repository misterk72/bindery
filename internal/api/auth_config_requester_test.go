package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/db"
)

// TestGetConfig_APIKeyNeverForRequesters is defence in depth behind the
// middleware fix: whatever stamped the admin role on the context, a request
// naming a user whose stored role is requester does not get the API key.
// Admin and user behave as before: the key follows the context role.
func TestGetConfig_APIKeyNeverForRequesters(t *testing.T) {
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	users := db.NewUserRepo(database)
	settings := db.NewSettingsRepo(database)
	if err := settings.Set(ctx, SettingAuthAPIKey, "the-api-key"); err != nil {
		t.Fatal(err)
	}
	admin, _ := users.Create(ctx, "admin", "x")
	_ = users.SetRole(ctx, admin.ID, auth.RoleAdmin)
	plain, _ := users.Create(ctx, "plain", "x")
	reader, _ := users.Create(ctx, "reader", "x")
	_ = users.SetRole(ctx, reader.ID, auth.RoleRequester)
	h := NewAuthHandler(users, settings, auth.NewLoginLimiter(10, time.Minute))

	cases := []struct {
		name    string
		uid     int64
		role    string
		wantKey bool
	}{
		{"install request, no user", 0, auth.RoleAdmin, true},
		{"admin session", admin.ID, auth.RoleAdmin, true},
		{"requester stamped admin", reader.ID, auth.RoleAdmin, false},
		{"user stamped admin", plain.ID, auth.RoleAdmin, true},
		{"requester", reader.ID, auth.RoleRequester, false},
		{"user", plain.ID, auth.RoleUser, false},
	}
	for _, c := range cases {
		reqCtx := auth.WithUserRole(ctx, c.role)
		if c.uid != 0 {
			reqCtx = auth.WithUserID(reqCtx, c.uid)
		}
		rec := httptest.NewRecorder()
		h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil).WithContext(reqCtx))
		if got := strings.Contains(rec.Body.String(), "the-api-key"); got != c.wantKey {
			t.Errorf("%s: key returned = %v, want %v (body %s)", c.name, got, c.wantKey, rec.Body.String())
		}
	}
}
