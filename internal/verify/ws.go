// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// WSPath is the dashboard live feed of Snapshot JSON (🎯T139).
const WSPath = "/ws/verify"

const snapshotWriteTimeout = 5 * time.Second

// HandleWS upgrades to a WebSocket and pushes Snapshot JSON on connect
// and whenever a run changes. Clients do not poll; this is the Verify
// tab's live path.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		defer cancel()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	updates, unsub := h.subscribe()
	defer unsub()

	if err := h.writeSnapshot(ctx, c); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
			if err := h.writeSnapshot(ctx, c); err != nil {
				return
			}
		}
	}
}

func (h *Hub) writeSnapshot(ctx context.Context, c *websocket.Conn) error {
	b, err := json.Marshal(h.Snapshot())
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, snapshotWriteTimeout)
	defer cancel()
	return c.Write(wctx, websocket.MessageText, b)
}
