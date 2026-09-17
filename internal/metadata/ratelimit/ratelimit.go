// Package ratelimit holds the one error every metadata provider client marks
// a rate limit refusal with. It is a leaf package so the provider clients and
// the aggregator can share it without importing each other.
package ratelimit

import "errors"

// ErrRateLimited matches, through errors.Is, any error a metadata provider
// client returns because the provider refused to answer for rate limit
// reasons: an OpenLibrary HTTP 429 that outlived its retries, or a Hardcover
// throttle refusal. Callers that walk many authors (scheduled discovery,
// #2236) use it to stop instead of burning through their queue.
var ErrRateLimited = errors.New("metadata provider rate limited")
