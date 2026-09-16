package migrate

import (
	"errors"
	"fmt"
	"testing"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
)

// TestPrimaryOutageIgnoresThrottling is the v1.36.2 review finding on #2613:
// Hardcover's pacer fails a lookup fast with an error matching
// hardcover.ErrRateLimited once its backoff would outlast the deadline. That
// is Hardcover asking us to slow down, not Hardcover being down, so it must
// not trip the breaker that fails every remaining row.
func TestPrimaryOutageIgnoresThrottling(t *testing.T) {
	throttled := metadata.SearchOutcome{
		Primary:         "hardcover",
		FailedProviders: []string{"hardcover"},
		PrimaryFailed:   true,
		FirstErr:        fmt.Errorf("hardcover: %w", hardcover.ErrRateLimited),
	}
	down := metadata.SearchOutcome{
		Primary:         "hardcover",
		FailedProviders: []string{"hardcover"},
		PrimaryFailed:   true,
		FirstErr:        errors.New("hardcover: context deadline exceeded"),
	}

	p := &primaryOutage{}
	for range primaryOutageThreshold + 2 {
		p.observe("csv", throttled)
	}
	if p.down() {
		t.Fatal("throttled lookups tripped the outage breaker")
	}

	// Throttling in between real failures neither extends nor resets the
	// streak.
	p.observe("csv", down)
	p.observe("csv", throttled)
	p.observe("csv", down)
	if p.down() {
		t.Fatal("tripped after two real failures")
	}
	p.observe("csv", down)
	if !p.down() {
		t.Error("three real failures with throttling between them did not trip the breaker")
	}
}
