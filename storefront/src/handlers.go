package main

import (
	"net/http"
	"runtime"
)

// Product is a catalogue entry.
//
// Prices are integer minor units - pence, cents - never a float. A float price
// is wrong by a fraction of a penny per operation, and the day that matters is
// the day someone reconciles a ledger.
type Product struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Price    int    `json:"price_minor_units"`
	Currency string `json:"currency"`
	InStock  bool   `json:"in_stock"`
}

// Static fixtures. This service has no database on purpose: apps-business#1 is
// about proving the delivery path - build, push, promote, deploy - and a
// database would put a StatefulSet, a backup policy and an IRSA role on the
// critical path of a scaffolding ticket.
var catalogue = []Product{
	{ID: "sku-1001", Name: "Ubuntu 25c Hoodie", Price: 4500, Currency: "GBP", InStock: true},
	{ID: "sku-1002", Name: "Platform Team Mug", Price: 1200, Currency: "GBP", InStock: true},
	{ID: "sku-1003", Name: "Graviton Sticker Pack", Price: 350, Currency: "GBP", InStock: false},
}

func (a *app) handleProducts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"products": catalogue,
		"count":    len(catalogue),
	})
}

func (a *app) handleProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	for _, p := range catalogue {
		if p.ID == id {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}

	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": "no product with that id",
		"id":    id,
	})
}

// handleHealth reports liveness: is the process running at all.
//
// It must not check dependencies. A liveness probe that fails when a downstream
// is unavailable causes kubelet to restart a perfectly healthy pod, which turns
// a partial outage into a crash loop and removes the capacity that was still
// serving. Dependency checks belong in readiness.
func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"version":    a.version,
		"go_version": runtime.Version(),
		"arch":       runtime.GOARCH,
	})
}

// handleReady reports readiness: should traffic be routed here right now.
//
// Returns 503 once shutdown has begun, which is what takes this pod out of
// Service backends before connections start closing.
func (a *app) handleReady(w http.ResponseWriter, r *http.Request) {
	if !a.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "draining",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
