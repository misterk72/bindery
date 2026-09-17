package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// Review item 1b: three authors failing in a row means the provider is down,
// not the authors. The pass stops and those three keep their place.
func TestDiscoveryTick_BreakerStopsAfterThreeConsecutiveFailures(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A", "OL3A", "OL4A", "OL5A")
	f.job.interval = func() (time.Duration, bool) { return time.Hour, true }
	for _, id := range []string{"OL1A", "OL2A", "OL3A", "OL4A", "OL5A"} {
		f.disc.outcomes[id] = DiscoveryOutcome{Err: errors.New("HTTP 502")}
	}

	res := f.job.tick(context.Background())
	if !res.Breaker {
		t.Fatalf("tick = %+v, want the breaker to trip", res)
	}
	if len(f.disc.calls) != 3 {
		t.Fatalf("discoverer called for %v, want the pass to stop after 3", f.disc.calls)
	}
	for _, id := range []string{"OL1A", "OL2A", "OL3A", "OL4A", "OL5A"} {
		if got := f.cursor(t, id); got != nil {
			t.Errorf("%s stamped at %v; a provider outage must not cost authors their week", id, got)
		}
	}
}

// An isolated failure is stamped, so one author that always fails cannot hold
// the front of the queue; failures a success interrupts never trip the breaker.
func TestDiscoveryTick_IsolatedFailuresAreStamped(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A", "OL3A", "OL4A", "OL5A")
	f.job.interval = func() (time.Duration, bool) { return time.Hour, true }
	f.disc.outcomes["OL1A"] = DiscoveryOutcome{Err: errors.New("poison author")}
	f.disc.outcomes["OL3A"] = DiscoveryOutcome{Err: errors.New("HTTP 502")}
	f.disc.outcomes["OL4A"] = DiscoveryOutcome{Err: errors.New("HTTP 502")}

	res := f.job.tick(context.Background())
	if res.Breaker || res.Checked != 5 {
		t.Fatalf("tick = %+v, want all 5 checked and no breaker", res)
	}
	for _, id := range []string{"OL1A", "OL2A", "OL3A", "OL4A", "OL5A"} {
		if f.cursor(t, id) == nil {
			t.Errorf("%s was not stamped", id)
		}
	}
}

// Two failures that end the pass are isolated too.
func TestDiscoveryTick_FailuresAtPassEndAreStamped(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A")
	f.job.interval = func() (time.Duration, bool) { return time.Hour, true }
	f.disc.outcomes["OL1A"] = DiscoveryOutcome{Err: errors.New("x")}
	f.disc.outcomes["OL2A"] = DiscoveryOutcome{Err: errors.New("y")}
	f.job.tick(context.Background())
	for _, id := range []string{"OL1A", "OL2A"} {
		if f.cursor(t, id) == nil {
			t.Errorf("%s was not stamped", id)
		}
	}
}

// Review item 2: an author that runs past its budget is cut off, stamped as
// checked, and the pass moves on.
func TestDiscoveryTick_AuthorOverBudgetIsStampedAndPassContinues(t *testing.T) {
	prev := discoveryAuthorBudget
	discoveryAuthorBudget = 30 * time.Millisecond
	defer func() { discoveryAuthorBudget = prev }()

	f := newDiscoveryJobFixture(t, "OL1A", "OL2A")
	f.job.interval = func() (time.Duration, bool) { return time.Hour, true }
	f.disc.run = func(ctx context.Context, a *models.Author, out *DiscoveryOutcome) {
		if a.ForeignID != "OL1A" {
			return
		}
		select {
		case <-ctx.Done():
			out.Err = ctx.Err()
		case <-time.After(5 * time.Second):
			t.Error("the per author budget never cancelled the run")
		}
	}

	res := f.job.tick(context.Background())
	if res.TimedOut != 1 || res.Checked != 2 {
		t.Fatalf("tick = %+v, want 1 timed out and both authors checked", res)
	}
	for _, id := range []string{"OL1A", "OL2A"} {
		if f.cursor(t, id) == nil {
			t.Errorf("%s was not stamped", id)
		}
	}
}

// Review item 3: a bulk refresh that starts mid pass stops the pass before
// the next author.
func TestDiscoveryTick_StopsWhenBulkRefreshStartsMidPass(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A", "OL3A")
	f.job.interval = func() (time.Duration, bool) { return time.Hour, true }
	f.disc.run = func(_ context.Context, a *models.Author, _ *DiscoveryOutcome) {
		if a.ForeignID == "OL1A" {
			f.disc.mu.Lock()
			f.disc.bulk = true
			f.disc.mu.Unlock()
		}
	}

	res := f.job.tick(context.Background())
	if res.Stopped == "" || len(f.disc.calls) != 1 {
		t.Fatalf("tick = %+v with calls %v, want the pass stopped after the first author", res, f.disc.calls)
	}
	if f.cursor(t, "OL1A") == nil {
		t.Error("the author checked before the bulk refresh started was not stamped")
	}
	if f.cursor(t, "OL2A") != nil {
		t.Error("an author after the bulk refresh started was stamped")
	}
}

// Review item 6: with a batch of one, a busy front author must not leave the
// tick idle. The next due author is checked in the same tick.
func TestDiscoveryTick_BusyAuthorUsesASpareCandidate(t *testing.T) {
	f := newDiscoveryJobFixture(t, "OL1A", "OL2A", "OL3A")
	// Three authors over a week gives a batch of one.
	if got := discoveryBatchSize(3, 168*time.Hour); got != 1 {
		t.Fatalf("fixture assumption: batch = %d, want 1", got)
	}
	f.disc.outcomes["OL1A"] = DiscoveryOutcome{Busy: true}

	res := f.job.tick(context.Background())
	if res.Checked != 1 {
		t.Fatalf("tick = %+v, want one author checked despite the busy one", res)
	}
	if f.cursor(t, "OL2A") == nil {
		t.Error("the spare author was not checked")
	}
	if f.cursor(t, "OL3A") != nil {
		t.Error("the tick checked more than its batch of one")
	}
}
