package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
	"github.com/vavallee/bindery/internal/models"
)

// The wiring is where provider and handler errors become the scheduler's
// flags (#2236). A rate limit must back off, a running sync must count as
// busy, and neither must be mistaken for the other or for an ordinary error.
func TestNewAuthorDiscoverer_MapsErrors(t *testing.T) {
	cases := []struct {
		name        string
		created     int
		err         error
		wantBackoff bool
		wantBusy    bool
	}{
		{name: "success", created: 3},
		{name: "ordinary error", err: errors.New("provider 500")},
		{name: "hardcover rate limit, wrapped", err: fmt.Errorf("author works: %w", hardcover.ErrRateLimited), wantBackoff: true},
		{name: "openlibrary rate limit, wrapped", err: fmt.Errorf("author works: %w", openlibrary.ErrRateLimited), wantBackoff: true},
		{name: "sync already running", err: api.ErrAuthorSyncRunning, wantBusy: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newAuthorDiscoverer(func(context.Context, *models.Author) (int, error) {
				return tc.created, tc.err
			}, nil)
			out := d.DiscoverAuthor(context.Background(), &models.Author{ID: 1})
			if out.Created != tc.created || !errors.Is(out.Err, tc.err) || out.Backoff != tc.wantBackoff || out.Busy != tc.wantBusy {
				t.Errorf("outcome = %+v, want created %d, backoff %v, busy %v", out, tc.created, tc.wantBackoff, tc.wantBusy)
			}
		})
	}
}

func TestNewAuthorDiscoverer_BulkRunning(t *testing.T) {
	noop := func(context.Context, *models.Author) (int, error) { return 0, nil }
	if newAuthorDiscoverer(noop, nil).BulkRefreshRunning() {
		t.Error("nil bulk check reported a running bulk refresh")
	}
	if !newAuthorDiscoverer(noop, func() bool { return true }).BulkRefreshRunning() {
		t.Error("bulk check was not consulted")
	}
}
