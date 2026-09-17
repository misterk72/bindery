package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vavallee/bindery/internal/auth"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// Tests for the findings of the security review of #2670.

// slowMeta answers every lookup with a book or author after a delay, so a
// check then act gap around the provider call is wide enough to hit.
type slowMeta struct{ delay time.Duration }

func (m slowMeta) GetBook(_ context.Context, id string) (*models.Book, error) {
	time.Sleep(m.delay)
	return &models.Book{ForeignID: id, Title: "Book " + id}, nil
}

func (m slowMeta) GetAuthor(_ context.Context, id string) (*models.Author, error) {
	time.Sleep(m.delay)
	return &models.Author{ForeignID: id, Name: "Author " + id}, nil
}

// Finding 1: POST /requests reaches the provider on every call, and a failed
// lookup stores nothing, so the pending cap never fills. The handler spends
// the provider bucket itself: from one requester, rapid creates past the
// burst of 20 answer 429, even with ids the provider does not know.
func TestRequestsCreate_RateLimitedInHandler(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	f.h.WithProviderLimiter(auth.NewRequesterLimiter(20, 1.0/3.0, time.Minute, 64))
	codes := make([]int, 0, 22)
	for i := 0; i < 22; i++ {
		rec := f.create(t, f.requester, fmt.Sprintf(`{"kind":"book","foreignId":"OL-FAKE-%d"}`, i))
		codes = append(codes, rec.Code)
	}
	for i, c := range codes[:20] {
		if c == http.StatusTooManyRequests {
			t.Fatalf("create %d inside the burst answered 429: %v", i+1, codes)
		}
	}
	if codes[21] != http.StatusTooManyRequests {
		t.Fatalf("22nd rapid create: %d, want 429 (all: %v)", codes[21], codes)
	}
	// An admin is not limited by the requester bucket.
	if rec := f.create(t, f.admin, `{"kind":"book","foreignId":"OL27482W"}`); rec.Code == http.StatusTooManyRequests {
		t.Fatalf("admin create limited: %d", rec.Code)
	}
}

// Finding 2: count then insert across a provider round trip let concurrent
// creates overshoot the cap. Run with -race.
func TestRequestsCreate_PendingCapHoldsUnderConcurrency(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	h := NewRequestHandler(f.requests, f.books, f.authors, f.settings, slowMeta{delay: 50 * time.Millisecond}, &fakeAdder{}).
		WithProviderLimiter(auth.NewRequesterLimiter(1<<20, 1<<20, time.Minute, 64))
	if err := f.settings.Set(context.Background(), SettingRequestsMaxPendingPerUser, "3"); err != nil {
		t.Fatal(err)
	}
	const racers = 24
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := asUser(newJSONRequest(http.MethodPost, "/api/v1/requests", fmt.Sprintf(`{"kind":"book","foreignId":"OL%dW"}`, i)), f.requester)
			rec := newRecorder()
			h.Create(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	n, err := f.requests.CountPendingByOwner(context.Background(), f.requester.ID)
	if err != nil {
		t.Fatal(err)
	}
	created, capped := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusTooManyRequests:
			capped++
		default:
			t.Errorf("unexpected status %d", c)
		}
	}
	if n != 3 || created != 3 || capped != racers-3 {
		t.Fatalf("pending %d, 201s %d, 429s %d; want 3, 3, %d", n, created, capped, racers-3)
	}
}

// Finding 3: create then withdraw in a loop sent one webhook per create.
// Repeats for the same (owner, kind, foreign id) within the window send
// nothing; a different request still notifies.
func TestRequestsNotify_RepeatWithinWindowSuppressed(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	capture := &countingNotifier{}
	f.h.WithNotifier(capture, f.users)

	for i := 0; i < 5; i++ {
		rec := f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %d: %d %s", i, rec.Code, rec.Body.String())
		}
		created := decodeRequest(t, rec)
		wrec := newRecorder()
		f.h.Withdraw(wrec, withID(asUser(newJSONRequest(http.MethodDelete, "/", ""), f.requester), created.ID))
		if wrec.Code != http.StatusNoContent {
			t.Fatalf("withdraw %d: %d", i, wrec.Code)
		}
	}
	if rec := f.create(t, f.requester, `{"kind":"author","foreignId":"OL39307A"}`); rec.Code != http.StatusCreated {
		t.Fatalf("author create: %d", rec.Code)
	}
	// Sends run in goroutines; give them time to land.
	time.Sleep(500 * time.Millisecond)
	got := capture.snapshot()
	if len(got) != 2 {
		t.Fatalf("sent %d events, want 2 (one per distinct request): %v", len(got), got)
	}
	for _, e := range got {
		if e != notifier.EventRequestCreated {
			t.Fatalf("event %q", e)
		}
	}
}

type countingNotifier struct {
	mu     sync.Mutex
	events []string
}

func (c *countingNotifier) Send(_ context.Context, eventType string, _ map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, eventType)
}

func (c *countingNotifier) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...)
}

func TestRecentKeys_WindowAndBound(t *testing.T) {
	k := newRecentKeys(time.Hour, 3)
	now := time.Unix(1_700_000_000, 0)
	k.now = func() time.Time { return now }
	if !k.first("a") || k.first("a") {
		t.Fatal("a: want first then suppressed")
	}
	for _, key := range []string{"b", "c", "d", "e"} {
		k.first(key)
		if len(k.seen) > 3 {
			t.Fatalf("map holds %d keys, bound is 3", len(k.seen))
		}
	}
	now = now.Add(2 * time.Hour)
	if !k.first("e") {
		t.Fatal("e after the window: want it to fire again")
	}
}

// Finding 7: a claim that went stale while a slow add ran could be retaken by
// a second approval, which then released it or completed it, and the first
// approval's Complete answered 500. The running approval now renews its
// claim, so the second approval is refused and the first completes.
func TestRequestsApprove_SlowCoreKeepsClaim(t *testing.T) {
	adder := &fakeAdder{hold: time.Second}
	f := newRequestsFixture(t, wellsRequestStub(), adder)
	f.requests.WithClaimTTL(300 * time.Millisecond)
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))

	first := make(chan int, 1)
	go func() { first <- f.approve(f.admin, created.ID, `{}`).Code }()
	time.Sleep(600 * time.Millisecond)
	if code := f.approve(f.admin, created.ID, `{}`).Code; code != http.StatusConflict {
		t.Fatalf("second approval while the first is still adding: %d, want 409", code)
	}
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first approval: %d, want 200", code)
	}
	if n := adder.calls.Load(); n != 1 {
		t.Fatalf("add ran %d times, want once", n)
	}
	if row, _ := f.requests.GetByID(context.Background(), created.ID); row.Status != models.RequestStatusApproved {
		t.Fatalf("status %q, want approved", row.Status)
	}
}

// Finding 8: encoding/json matches keys case insensitively, so "Kind" beside
// "kind" (or alone) used to be accepted. Keys must match exactly.
func TestRequests_RejectCaseFoldedKeys(t *testing.T) {
	f := newRequestsFixture(t, wellsRequestStub(), &fakeAdder{})
	for _, body := range []string{
		`{"kind":"book","Kind":"author","foreignId":"OL27482W"}`,
		`{"Kind":"book","foreignId":"OL27482W"}`,
		`{"kind":"book","foreignId":"OL27482W","FOREIGNID":"OL1W"}`,
		`{"kind":"book","foreignid":"OL27482W"}`,
	} {
		if rec := f.create(t, f.requester, body); rec.Code != http.StatusBadRequest {
			t.Errorf("create body %s: %d, want 400", body, rec.Code)
		}
	}
	created := decodeRequest(t, f.create(t, f.requester, `{"kind":"book","foreignId":"OL27482W"}`))
	for _, body := range []string{`{"SearchOnAdd":true}`, `{"searchOnAdd":false,"searchonadd":true}`} {
		if rec := f.approve(f.admin, created.ID, body); rec.Code != http.StatusBadRequest {
			t.Errorf("approve body %s: %d, want 400", body, rec.Code)
		}
	}
}

func newJSONRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }
