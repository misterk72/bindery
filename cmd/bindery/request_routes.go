package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vavallee/bindery/internal/auth"
)

// requestRouteHandler is the surface registerRequestRoutes needs.
type requestRouteHandler interface {
	Create(http.ResponseWriter, *http.Request)
	ListMine(http.ResponseWriter, *http.Request)
	Withdraw(http.ResponseWriter, *http.Request)
	Library(http.ResponseWriter, *http.Request)
	Queue(http.ResponseWriter, *http.Request)
	PendingCount(http.ResponseWriter, *http.Request)
	Approve(http.ResponseWriter, *http.Request)
	Decline(http.ResponseWriter, *http.Request)
}

// registerRequestRoutes mounts /requests. The first four routes are the
// requester's own surface and are the /requests entries on
// auth.RequesterAllowList; the handlers scope every one of them to the caller.
// The queue, the count and the decisions are admin only: approving runs an
// add with the requester as owner, which only an admin may authorise.
func registerRequestRoutes(r chi.Router, h requestRouteHandler) {
	r.Get("/requests", h.ListMine)
	r.Post("/requests", h.Create)
	r.Delete("/requests/{id}", h.Withdraw)
	r.Get("/requests/library", h.Library)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		r.Get("/requests/queue", h.Queue)
		r.Get("/requests/pending-count", h.PendingCount)
		r.Post("/requests/{id}/approve", h.Approve)
		r.Post("/requests/{id}/decline", h.Decline)
	})
}
