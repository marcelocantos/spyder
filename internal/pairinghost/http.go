// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package pairinghost

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/marcelocantos/pigeon/qr"
)

// Handler exposes the pairing host over HTTP for the dashboard and
// device-inject tooling (🎯T93.1).
//
//	POST /stream/pair              — begin ceremony → {id, token, status}
//	GET  /stream/pair/{id}         — session status (+ SAS code when ready)
//	GET  /stream/pair/{id}/qr      — Unicode QR of the rendezvous token
//	POST /stream/pair/{id}/confirm — operator confirms SAS match
//	POST /stream/pair/{id}/abort   — cancel ceremony
//	GET  /stream/pairings          — known peer instance IDs
type Handler struct {
	Host *Host
}

// Mount registers routes on mux. Paths match the list above.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/stream/pair", h.handlePairRoot)
	mux.HandleFunc("/stream/pair/", h.handlePairID)
	mux.HandleFunc("/stream/pairings", h.handlePairings)
}

func (h *Handler) handlePairRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Host == nil {
		http.Error(w, "pairing host not configured", http.StatusServiceUnavailable)
		return
	}
	sess, err := h.Host.Begin(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":          sess.ID,
		"token":       sess.Token,
		"status":      sess.Status,
		"created_at":  sess.CreatedAt,
		"relay":       h.Host.RelayURL(),
		"instance_id": h.Host.Identity().InstanceID(),
		// Player must receive `token` via spyder QR/inject — never invent it.
		"delivery": "spyder_qr_or_inject",
	})
}

func (h *Handler) handlePairID(w http.ResponseWriter, r *http.Request) {
	if h.Host == nil {
		http.Error(w, "pairing host not configured", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/stream/pair/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	switch {
	case sub == "" && r.Method == http.MethodGet:
		sess := h.Host.Get(id)
		if sess == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, sess)

	case sub == "qr" && r.Method == http.MethodGet:
		sess := h.Host.Get(id)
		if sess == nil {
			http.NotFound(w, r)
			return
		}
		if sess.Token == "" {
			http.Error(w, "no token", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		// Token is the full rendezvous credential (nonce material for the
		// player). Rendering QR is spyder's job in this modality.
		qr.Print(w, sess.Token)

	case sub == "confirm" && r.Method == http.MethodPost:
		sess, err := h.Host.Confirm(r.Context(), id)
		if err != nil {
			status := http.StatusBadRequest
			if sess == nil {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, sess)

	case sub == "abort" && r.Method == http.MethodPost:
		if err := h.Host.Abort(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "aborted"})

	default:
		http.Error(w, fmt.Sprintf("unsupported %s %s", r.Method, r.URL.Path), http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handlePairings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Host == nil {
		http.Error(w, "pairing host not configured", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instance_id": h.Host.Identity().InstanceID(),
		"relay":       h.Host.RelayURL(),
		"peers":       h.Host.Store().List(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
