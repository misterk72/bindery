package metadata

import (
	"context"

	"github.com/vavallee/bindery/internal/metadata/ratelimit"
	"github.com/vavallee/bindery/internal/models"
)

// ErrRateLimited matches, through errors.Is, any provider refusal for rate
// limit reasons, whichever provider refused. See package ratelimit.
var ErrRateLimited = ratelimit.ErrRateLimited

// deferCoverEnrichmentKey is the context key WithDeferredCoverEnrichment sets.
type deferCoverEnrichmentKey struct{}

// WithDeferredCoverEnrichment marks ctx so GetAuthorWorksForAuthor skips its
// per work cover enrichment and leaves it to the caller (#2236).
//
// That enrichment is one enricher round trip, plus an edition sample, for
// every coverless work the author has, and #2578 measured it in the thousands
// for a prolific author. A catalogue sync that already has most of those
// works in the library stores none of the covers it finds for them, so the
// scheduled discovery job enriches only the works it may create, through
// EnrichMissingCovers.
//
// A list fetched this way is never cached, because the cached catalogue is
// served to callers that expect covers. A cached, enriched list is still
// served as it is.
func WithDeferredCoverEnrichment(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferCoverEnrichmentKey{}, true)
}

func coverEnrichmentDeferred(ctx context.Context) bool {
	v, _ := ctx.Value(deferCoverEnrichmentKey{}).(bool)
	return v
}

// EnrichMissingCovers runs the cover enrichment GetAuthorWorksForAuthor would
// have run, over books only. It fills ImageURL in place on books that have
// none.
func (a *Aggregator) EnrichMissingCovers(ctx context.Context, books []models.Book) {
	if a == nil || len(books) == 0 {
		return
	}
	a.enrichMissingAuthorWorkCovers(ctx, books)
}
