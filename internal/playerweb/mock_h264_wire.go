// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package playerweb

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type h264WireState struct {
	mu         sync.Mutex
	deviceCaps uint8
	transport  uint8
	framesSent int
}

func (s *h264WireState) snapshot() (caps uint8, transport uint8, frames int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deviceCaps, s.transport, s.framesSent
}

func registerMockH264Wire(mux *http.ServeMux, aus [][]byte, st *h264WireState) {
	mux.HandleFunc("/ws/wire", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()

		if err := c.Write(ctx, websocket.MessageBinary, buildSessionConfigMessage(kTransportH264)); err != nil {
			return
		}
		st.mu.Lock()
		st.transport = kTransportH264
		st.mu.Unlock()

		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			if len(msg) < 8+deviceInfoWireSize {
				continue
			}
			if binary.LittleEndian.Uint32(msg) != kDeviceInfoMagic {
				continue
			}
			caps := msg[8+deviceInfoCapOffset]
			st.mu.Lock()
			st.deviceCaps = caps
			st.mu.Unlock()
			slog.Info("mock wire: DeviceInfo", "caps", caps)
			streamH264(ctx, c, aus, st)
			return
		}
	})
}

func streamH264(ctx context.Context, c *websocket.Conn, aus [][]byte, st *h264WireState) {
	const fps = 60
	tick := time.NewTicker(time.Second / time.Duration(fps))
	defer tick.Stop()
	deadline := time.Now().Add(75 * time.Second)
	var seq uint32
	for time.Now().Before(deadline) {
		au := aus[int(seq)%len(aus)]
		avcc, err := annexBToAVCC(au)
		if err != nil {
			return
		}
		key := accessUnitIsKeyframe(au)
		frame := buildSP2VMessage(avcc, seq, key)
		if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
			return
		}
		st.mu.Lock()
		st.framesSent++
		st.mu.Unlock()
		seq++
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
