package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// mountUnderURLBase serves r under BINDERY_URL_BASE, or returns r unchanged
// when base is empty. Lifted out of main() unchanged so the route tests can
// put the real prefix handling in front of the auth stack: the requester
// allow list matches on r.URL.Path, so it has to see the path after the prefix
// is gone.
func mountUnderURLBase(r http.Handler, base string) http.Handler {
	if base == "" {
		return r
	}
	outer := chi.NewRouter()
	// Redirect bare prefix (no trailing slash) to prefix/ so the SPA
	// bootstrap and asset resolution work correctly.
	outer.Get(base, http.RedirectHandler(base+"/", http.StatusMovedPermanently).ServeHTTP)
	// http.StripPrefix actually rewrites r.URL.Path (and RawPath) before
	// dispatch, so the inner router sees un-prefixed paths. chi.Mount only
	// rewrites the routing-context path and leaves r.URL.Path prefixed, which
	// breaks the static file handler and http.FileServer: they read r.URL.Path
	// directly and would look up "<prefix>/assets/..." in the embedded FS,
	// miss, and fall back to serving index.html (text/html) for every JS/CSS
	// asset.
	outer.Handle(base+"/*", http.StripPrefix(base, r))
	return outer
}
