// Package providererr holds the errors every metadata provider client marks
// its failures with when the provider, not the request, is the problem. It is
// a leaf package so the provider clients and the aggregator can share it
// without importing each other.
package providererr

import "errors"

// ErrRateLimited matches, through errors.Is, any error a metadata provider
// client returns because the provider refused to answer for rate limit
// reasons: an OpenLibrary HTTP 429 that outlived its retries, or a Hardcover
// throttle refusal. Callers that walk many authors (scheduled discovery,
// #2236) use it to stop instead of burning through their queue.
var ErrRateLimited = errors.New("metadata provider rate limited")

// ErrUnavailable matches, through errors.Is, an error a metadata provider
// client returns because the provider answered with a server error (HTTP 5xx)
// that outlived its retries. Together with rate limits, network errors and
// timeouts it tells "the provider is down" apart from "this one request has
// no answer", such as a not found or an unparseable record.
var ErrUnavailable = errors.New("metadata provider unavailable")
