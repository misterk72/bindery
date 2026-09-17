package hardcover

import (
	"errors"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/ratelimit"
)

// A Hardcover refusal matches the provider independent sentinel as well as
// its own, so a caller need not know which provider refused (#2236).
func TestRateLimited_MatchesSharedSentinel(t *testing.T) {
	err := fmt.Errorf("author works: %w", rateLimited(errors.New("HTTP 429")))
	if !errors.Is(err, ErrRateLimited) || !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("errors.Is against the sentinels = %v, %v; want both true",
			errors.Is(err, ErrRateLimited), errors.Is(err, ratelimit.ErrRateLimited))
	}
	if errors.Is(errors.New("HTTP 500"), ratelimit.ErrRateLimited) {
		t.Fatal("a plain error matched the rate limit sentinel")
	}
}
