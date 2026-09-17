package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/models"
)

// blockingSearcher holds every search until release is closed, so a test can
// look at what a sync still holds while its indexer searches run.
type blockingSearcher struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSearcher) SearchAndGrabBook(context.Context, models.Book) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
}

// Second review, item 2a: AddBook's single work fallback does not wait on the
// per author lock. Its caller gives up after 15 seconds, and a discovery run
// can hold the lock for minutes.
func TestSingleWorkFallback_DoesNotWaitForTheAuthorLock(t *testing.T) {
	f := newDiscoveryFixture(t, true)
	h := f.handler(&stubMetaProvider{works: []models.Book{existingWork(), discoveryWork(1)}}, &eventRecorder{})

	unlock, err := h.catalogueWrites.lock(context.Background(), f.author.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	done := make(chan error, 1)
	go func() {
		_, err := h.runCatalogueSync(context.Background(), f.author, catalogueSyncOptions{
			mediaType: models.MediaTypeEbook, onlyForeignID: "OL2236W1",
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("single work fallback: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the single work fallback waited on the lock another sync of the author holds")
	}
	if b, _ := f.books.GetByForeignID(context.Background(), "OL2236W1"); b == nil {
		t.Fatal("the fallback did not create the requested book")
	}
}

// Second review, item 2b: the lock is released before indexer searches, so a
// sync that searches for its new books does not hold up the next sync of the
// same author.
func TestCatalogueSync_ReleasesTheAuthorLockBeforeSearches(t *testing.T) {
	f := newDiscoveryFixture(t, true)
	searcher := &blockingSearcher{entered: make(chan struct{}, 1), release: make(chan struct{})}
	h := NewAuthorHandler(f.authors, nil, f.books, nil,
		metadata.NewAggregator(&stubMetaProvider{works: []models.Book{existingWork(), discoveryWork(1)}}),
		nil, f.profile, searcher)

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		h.FetchAuthorBooks(f.author, true, models.MediaTypeEbook)
	}()
	defer func() {
		close(searcher.release)
		<-finished
	}()
	select {
	case <-searcher.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the sync never searched for its new book")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := h.catalogueWrites.lock(ctx, f.author.ID)
	if err != nil {
		t.Fatalf("the author lock is still held while indexer searches run: %v", err)
	}
	unlock()
}

// Second review, item 4: waiting for the lock ends when the context does, so a
// shutdown is not queued behind a holder.
func TestAuthorCatalogueLock_WaitEndsWithContext(t *testing.T) {
	var locks authorCatalogueLocks
	unlock, err := locks.lock(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := locks.lock(ctx, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock with a cancelled context = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a cancelled waiter still waited")
	}

	unlock()
	again, err := locks.lock(context.Background(), 7)
	if err != nil {
		t.Fatalf("lock after the holder released: %v", err)
	}
	again()
	locks.mu.Lock()
	left := len(locks.locks)
	locks.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d lock entries left after every holder and waiter finished", left)
	}
}

// And through a real sync: a sync waiting on the lock returns with the
// context's error and creates nothing.
func TestCatalogueSync_WaitingForTheLockHonoursCancellation(t *testing.T) {
	f := newDiscoveryFixture(t, true)
	h := f.handler(&stubMetaProvider{works: []models.Book{existingWork(), discoveryWork(1)}}, &eventRecorder{})
	unlock, err := h.catalogueWrites.lock(context.Background(), f.author.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = h.runCatalogueSync(ctx, f.author, catalogueSyncOptions{mediaType: models.MediaTypeEbook, discovery: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("sync waiting on a held lock returned %v, want the context's deadline", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the sync kept waiting after its context ended")
	}
	if b, _ := f.books.GetByForeignID(context.Background(), "OL2236W1"); b != nil {
		t.Fatal("a sync that never got the lock created a book")
	}
}

// Second review, item 6: the re-read skip happens before any provider call.
// The stale snapshot guard inside the sync would also stop the create, so
// only a fetch count shows the early skip is there.
func TestDiscoverAuthorBooks_UnmonitoredOnRereadFetchesNothing(t *testing.T) {
	f := newDiscoveryFixture(t, true)
	selected := *f.author
	current := *f.author
	current.Monitored = false
	if err := f.authors.Update(context.Background(), &current); err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	p := &coverCountingProvider{
		stubMetaProvider: &stubMetaProvider{works: []models.Book{existingWork(), discoveryWork(1)}},
		onList:           func() { fetches.Add(1) },
	}
	h := NewAuthorHandler(f.authors, nil, f.books, nil, metadata.NewAggregator(p), nil, f.profile, nil)
	if _, err := h.DiscoverAuthorBooks(context.Background(), &selected); err != nil {
		t.Fatal(err)
	}
	if n := fetches.Load(); n != 0 {
		t.Fatalf("works fetched %d times for an author the re-read found unmonitored, want 0", n)
	}
}
