package main

import (
	"context"
	"errors"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/scheduler"
)

// authorDiscoverFunc is the one AuthorHandler method the discovery wiring
// calls, named so a test can stand in for the handler.
type authorDiscoverFunc func(ctx context.Context, author *models.Author) (int, error)

// newAuthorDiscoverer adapts the author handler to the scheduler's discovery
// job (#2236). The scheduler imports neither internal/api nor the Hardcover
// client, so this is where their errors become flags: a Hardcover rate limit
// is Backoff (stop the pass, leave the cursor), an author whose sync is
// already running is Busy (skip it, leave the cursor), and anything else is
// an ordinary error the job logs and stamps past.
func newAuthorDiscoverer(discover authorDiscoverFunc, bulkRunning func() bool) scheduler.AuthorDiscoverer {
	return scheduler.AuthorDiscovererFuncs{
		Discover: func(ctx context.Context, author *models.Author) scheduler.DiscoveryOutcome {
			created, err := discover(ctx, author)
			return scheduler.DiscoveryOutcome{
				Created: created,
				Err:     err,
				Backoff: errors.Is(err, hardcover.ErrRateLimited),
				Busy:    errors.Is(err, api.ErrAuthorSyncRunning),
			}
		},
		BulkRunning: bulkRunning,
	}
}
