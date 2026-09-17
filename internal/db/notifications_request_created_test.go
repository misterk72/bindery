package db

import (
	"context"
	"testing"

	"github.com/vavallee/bindery/internal/models"
)

// on_request_created survives every statement NotificationRepo runs: create,
// get, list and update.
func TestNotificationRepo_OnRequestCreatedRoundTrip(t *testing.T) {
	database, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	repo := NewNotificationRepo(database)

	n := &models.Notification{Name: "hook", Type: "webhook", URL: "https://example.test/h", Method: "POST", OnRequestCreated: true, Enabled: true}
	if err := repo.Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByID(ctx, n.ID)
	if err != nil || got == nil || !got.OnRequestCreated || got.OnHealth {
		t.Fatalf("after create: %+v err %v, want OnRequestCreated only", got, err)
	}
	got.OnRequestCreated = false
	got.OnGrab = true
	if err := repo.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 1 || list[0].OnRequestCreated || !list[0].OnGrab {
		t.Fatalf("after update: %+v err %v", list, err)
	}
}
