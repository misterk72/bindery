package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
)

// IsProviderUnavailable separates the provider being down from an error about
// one record, which is what keeps broken authors from tripping discovery's
// failure breaker (#2236).
func TestIsProviderUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"rate limit", fmt.Errorf("x: %w", ErrRateLimited), true},
		{"server error", fmt.Errorf("x: %w", ErrUnavailable), true},
		{"provider call timeout", fmt.Errorf("x: %w", context.DeadlineExceeded), true},
		{"network", &url.Error{Op: "Get", URL: "https://openlibrary.org", Err: errors.New("connection refused")}, true},
		{"not found", errors.New("not found"), false},
		{"parse error", errors.New("invalid character '<' looking for beginning of value"), false},
		{"cancelled", context.Canceled, false},
	}
	for _, tc := range cases {
		if got := IsProviderUnavailable(tc.err); got != tc.want {
			t.Errorf("%s: IsProviderUnavailable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
