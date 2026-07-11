// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package pairinghost is spyder's pigeon pairing host for the stream
// modality (🎯T93 / 🎯T93.1).
//
// Boundary: spyder owns out-of-band delivery of the rendezvous
// credential to the player (dashboard QR + optional device inject).
// Ge does not render QR. After the ceremony completes, spyder retains
// the server-side PairingRecord for factory-spawned ge instances and
// is OFF the encrypted data path.
//
// Live ceremony (pairing.Pairer.Accept) requires `-tags pairinglive`
// so the default spyder build does not pull pigeon cwire/quic-go past
// the go-ios pin. Store, identity, QR surface, and HTTP routes compile
// in the default build; Begin returns a clear error without the tag.
package pairinghost

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marcelocantos/pigeon/crypto"
)

// DefaultRelay is the public pigeon relay used when Config.Relay is empty.
const DefaultRelay = "https://carrier-pigeon.fly.dev"

// Config configures a Host.
type Config struct {
	// Relay is the pigeon relay URL (https://…). Empty → DefaultRelay.
	Relay string
	// IdentityPath is where the long-term acceptor identity is stored.
	IdentityPath string
	// RecordsPath is the pairings.json map path.
	RecordsPath string
}

// Host holds identity + PairingRecords and (when built with pairinglive)
// drives acceptor-side pairing ceremonies.
type Host struct {
	cfg      Config
	identity crypto.Identity
	store    *Store

	// pairer is set only by pairinglive builds. Begin fails if nil.
	pairer ceremonyStarter

	mu       sync.Mutex
	sessions map[string]*Session
	seq      atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
}

// Session is one in-flight (or completed) pairing ceremony for the API.
type Session struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"` // rendezvous credential for QR / inject
	Status    string    `json:"status"` // pending | waiting_code | paired | aborted | error
	Code      string    `json:"code,omitempty"`
	PeerID    string    `json:"peer_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// live is the in-process ceremony handle (pairinglive only).
	live liveCeremony
}

// ceremonyStarter starts acceptor-side ceremonies. Implemented in host_live.go.
type ceremonyStarter interface {
	Accept(ctx context.Context) (liveCeremony, string, error) // cer, token, err
	Close() error
}

// liveCeremony is one in-flight pigeon pairing ceremony.
type liveCeremony interface {
	Code(ctx context.Context) (string, error)
	Confirm(ctx context.Context) (*crypto.PairingRecord, error)
	Close() error
}

// New loads (or creates) identity + pairings store. With the default build
// tag set, live ceremony is unavailable until rebuilt with -tags pairinglive.
func New(ctx context.Context, cfg Config) (*Host, error) {
	if cfg.Relay == "" {
		cfg.Relay = DefaultRelay
	}
	if cfg.IdentityPath == "" || cfg.RecordsPath == "" {
		return nil, fmt.Errorf("pairinghost: IdentityPath and RecordsPath are required")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.IdentityPath), 0o700); err != nil {
		return nil, err
	}
	identity, err := crypto.NewFileIdentity(cfg.IdentityPath)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	store, err := OpenStore(cfg.RecordsPath)
	if err != nil {
		return nil, err
	}
	hctx, cancel := context.WithCancel(ctx)
	h := &Host{
		cfg:      cfg,
		identity: identity,
		store:    store,
		sessions: map[string]*Session{},
		ctx:      hctx,
		cancel:   cancel,
	}
	if err := h.attachLivePairer(hctx); err != nil {
		cancel()
		return nil, err
	}
	slog.Info("pairinghost ready",
		"relay", cfg.Relay,
		"instance_id", identity.InstanceID(),
		"paired_peers", len(store.List()),
		"live_ceremony", h.pairer != nil)
	return h, nil
}

// Identity is the long-term acceptor identity (inject into ge for Register).
func (h *Host) Identity() crypto.Identity { return h.identity }

// Store returns the server-side PairingRecord store.
func (h *Host) Store() *Store { return h.store }

// RelayURL returns the configured relay.
func (h *Host) RelayURL() string { return h.cfg.Relay }

// Close aborts in-flight ceremonies and releases the pairer if any.
func (h *Host) Close() error {
	h.cancel()
	h.mu.Lock()
	for _, s := range h.sessions {
		if s.live != nil {
			_ = s.live.Close()
		}
	}
	h.mu.Unlock()
	if h.pairer != nil {
		return h.pairer.Close()
	}
	return nil
}

// Begin starts a new acceptor-side ceremony and returns its Session.
// Token is the rendezvous payload the player must receive out-of-band
// (QR scan or inject) — that delivery is spyder's responsibility.
func (h *Host) Begin(ctx context.Context) (*Session, error) {
	if h.pairer == nil {
		return nil, fmt.Errorf("pairing ceremony unavailable: rebuild spyder with -tags pairinglive (see docs/pigeon-pairing-host.md)")
	}
	cer, token, err := h.pairer.Accept(ctx)
	if err != nil {
		return nil, fmt.Errorf("pairer.Accept: %w", err)
	}
	id := fmt.Sprintf("pair-%d", h.seq.Add(1))
	sess := &Session{
		ID:        id,
		Token:     token,
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
		live:      cer,
	}
	h.mu.Lock()
	h.sessions[id] = sess
	h.mu.Unlock()

	go h.watchCode(sess)

	slog.Info("pairing ceremony begun",
		"session", id,
		"token_len", len(token))
	return sess, nil
}

func (h *Host) watchCode(sess *Session) {
	if sess.live == nil {
		return
	}
	code, err := sess.live.Code(h.ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[sess.ID] != sess {
		return
	}
	if err != nil {
		if sess.Status == "pending" || sess.Status == "waiting_code" {
			sess.Status = "error"
			sess.Error = err.Error()
		}
		return
	}
	if sess.Status == "pending" {
		sess.Status = "waiting_code"
	}
	sess.Code = code
}

// Get returns a live or completed session snapshot by id.
func (h *Host) Get(id string) *Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if s == nil {
		return nil
	}
	return copySession(s)
}

// Confirm finishes the ceremony after the operator verifies the SAS code.
// On success the server-side PairingRecord is persisted.
func (h *Host) Confirm(ctx context.Context, id string) (*Session, error) {
	h.mu.Lock()
	sess := h.sessions[id]
	h.mu.Unlock()
	if sess == nil || sess.live == nil {
		return nil, fmt.Errorf("unknown pairing session %q", id)
	}
	rec, err := sess.live.Confirm(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		sess.Status = "error"
		sess.Error = err.Error()
		return copySession(sess), err
	}
	if err := h.store.Put(rec); err != nil {
		sess.Status = "error"
		sess.Error = err.Error()
		return copySession(sess), err
	}
	sess.Status = "paired"
	sess.PeerID = rec.PeerInstanceID
	sess.Error = ""
	_ = sess.live.Close()
	sess.live = nil
	slog.Info("pairing complete", "session", id, "peer", rec.PeerInstanceID)
	return copySession(sess), nil
}

// Abort cancels an in-flight ceremony.
func (h *Host) Abort(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.sessions[id]
	if sess == nil {
		return fmt.Errorf("unknown pairing session %q", id)
	}
	if sess.live != nil {
		_ = sess.live.Close()
		sess.live = nil
	}
	if sess.Status != "paired" {
		sess.Status = "aborted"
	}
	return nil
}

func copySession(s *Session) *Session {
	cp := *s
	cp.live = nil
	return &cp
}
