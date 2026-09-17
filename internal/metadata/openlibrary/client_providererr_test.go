package openlibrary

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vavallee/bindery/internal/metadata/providererr"
)

// A 429 that is still a 429 after the retries is a rate limit, and callers
// that walk many authors must be able to tell it from any other failure
// without matching message text (#2236).
func TestGetJSON_ExhaustedRateLimitIsRateLimited(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{
		{status: http.StatusTooManyRequests, body: `{"error":"throttled"}`},
	}}
	c := &Client{http: &http.Client{Transport: rt}}

	err := c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if !errors.Is(err, ErrRateLimited) || !errors.Is(err, providererr.ErrRateLimited) {
		t.Fatalf("errors.Is(err, ErrRateLimited) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want the HTTP 429 message kept", err)
	}
}

func TestGetJSON_OtherExhaustedStatusIsNotRateLimited(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{
		{status: http.StatusServiceUnavailable, body: `{"error":"down"}`},
	}}
	c := &Client{http: &http.Client{Transport: rt}}

	err := c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if err == nil || errors.Is(err, ErrRateLimited) {
		t.Fatalf("a 503 must not read as a rate limit; err = %v", err)
	}
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, providererr.ErrUnavailable) {
		t.Fatalf("a 503 that outlived its retries must read as unavailable; err = %v", err)
	}
}

func TestGetJSON_NotFoundIsNotUnavailable(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{{status: http.StatusNotFound, body: `{}`}}}
	c := &Client{http: &http.Client{Transport: rt}}
	err := c.getJSON(context.Background(), "https://openlibrary.org/works/OL1W.json", new(struct{}))
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRateLimited) {
		t.Fatalf("a 404 is about the record, not the provider; err = %v", err)
	}
}

// The author works call joins its two endpoint errors; the rate limit mark
// has to survive the join so the aggregator hands it to the sync.
func TestGetAuthorWorks_RateLimitSurvivesTheJoinedError(t *testing.T) {
	rt := &scriptedRoundTripper{responses: []scriptedResponse{
		{status: http.StatusTooManyRequests, body: `{"error":"throttled"}`},
	}}
	c := &Client{http: &http.Client{Transport: rt}}

	_, err := c.GetAuthorWorks(context.Background(), "OL1A")
	if !errors.Is(err, providererr.ErrRateLimited) {
		t.Fatalf("errors.Is(err, providererr.ErrRateLimited) = false; err = %v", err)
	}
}
