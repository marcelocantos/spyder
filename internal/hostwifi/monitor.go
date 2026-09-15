// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package hostwifi

import (
	"context"
	"log/slog"
	"time"

	"github.com/marcelocantos/spyder/internal/appchannel"
	"github.com/marcelocantos/spyder/internal/health"
	"github.com/marcelocantos/spyder/internal/wedge"
)

// Tunables for the Wi-Fi monitor. Vars (not consts) so tests can shorten them.
var (
	pollInterval = 30 * time.Second
)

// HealthEntityID is the host-wifi row in the health model.
var HealthEntityID = health.ID{Kind: health.KindHost, Name: "wifi"}

// Deps are injectable seams for production and tests.
type Deps struct {
	ReadStatus func() (Status, error)
	AppChannel func() appchannel.ConnectivityReport
	UsbWedged  func() (bool, error)
	Confirm    func(ctx context.Context, message string) (bool, error)
	Bounce     func(ctx context.Context, device string) error
	Health     *health.Model
}

// ProductionDeps wires macOS probes and app-channel staleness checks.
func ProductionDeps(
	mgr *appchannel.Manager,
	launches func() map[appchannel.AppKey]time.Time,
	hm *health.Model,
) Deps {
	return Deps{
		ReadStatus: ReadStatus,
		AppChannel: func() appchannel.ConnectivityReport {
			return appchannel.ProbeConnectivity(mgr, launches(), time.Now())
		},
		UsbWedged: func() (bool, error) {
			wedged, _, _, err := wedge.IsWedged()
			return wedged, err
		},
		Confirm: ConfirmBounce,
		Bounce:  Bounce,
		Health:  hm,
	}
}

type episodeState struct {
	inEpisode      bool
	attempted      bool
	declined       bool
	consecutiveBad int
}

// RunMonitor polls app-channel reachability until ctx is cancelled. When
// devices appear unable to dial back (or fail ping on a live session) and
// Wi-Fi is associated, it prompts once per episode for confirmation before
// bouncing Wi-Fi.
func RunMonitor(ctx context.Context, deps Deps) {
	if deps.ReadStatus == nil {
		deps.ReadStatus = ReadStatus
	}
	if deps.AppChannel == nil {
		deps.AppChannel = func() appchannel.ConnectivityReport {
			return appchannel.ConnectivityReport{}
		}
	}
	if deps.UsbWedged == nil {
		deps.UsbWedged = func() (bool, error) { return false, nil }
	}
	if deps.Confirm == nil {
		deps.Confirm = ConfirmBounce
	}
	if deps.Bounce == nil {
		deps.Bounce = Bounce
	}

	if deps.Health != nil {
		deps.Health.Register(HealthEntityID, health.KindHost, health.Policy{
			MaxAttempts: 1,
			BaseBackoff: 30 * time.Second,
		})
	}

	var st episodeState
	reconcile(ctx, deps, &st, "startup")

	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reconcile(ctx, deps, &st, "poll")
		}
	}
}

func reconcile(ctx context.Context, deps Deps, st *episodeState, source string) {
	wifi, err := deps.ReadStatus()
	if err != nil {
		slog.Debug("hostwifi: status read skipped", "source", source, "error", err)
		clearEpisode(st, deps, "no Wi-Fi port")
		return
	}

	usbWedge, werr := deps.UsbWedged()
	if werr != nil {
		slog.Debug("hostwifi: usbmux wedge check failed", "error", werr)
	}

	channel := deps.AppChannel()
	partial := wifi.Connected && !usbWedge && channel.StressPresent()
	if partial {
		st.consecutiveBad++
	} else {
		st.consecutiveBad = 0
	}

	h := Evaluate(HypothesisInput{
		Wifi:           wifi,
		AppChannel:     channel,
		UsbWedge:       usbWedge,
		ConsecutiveBad: st.consecutiveBad - 1,
	})

	updateHealth(deps, h, st)

	if !h.Solid {
		if st.inEpisode && st.consecutiveBad == 0 {
			slog.Info("hostwifi: episode cleared", "source", source, "detail", h.Detail)
		}
		if st.consecutiveBad == 0 {
			clearEpisodeState(st)
		}
		if h.Detail != "" {
			slog.Debug("hostwifi: no action", "source", source, "detail", h.Detail)
		}
		return
	}

	newEpisode := !st.inEpisode
	st.inEpisode = true
	slog.Warn("hostwifi: app-channel + Wi-Fi hypothesis",
		"source", source, "detail", h.Detail, "new_episode", newEpisode)

	if st.declined {
		slog.Info("hostwifi: user declined this episode; not re-prompting", "source", source)
		return
	}
	if st.attempted {
		slog.Info("hostwifi: bounce already attempted this episode", "source", source)
		return
	}

	approved, cerr := deps.Confirm(ctx, h.UserPrompt)
	if cerr != nil {
		slog.Error("hostwifi: confirmation dialog failed", "error", cerr)
		markAttention(deps, "confirmation dialog failed: "+cerr.Error())
		return
	}
	if !approved {
		st.declined = true
		slog.Info("hostwifi: user cancelled Wi-Fi bounce", "source", source)
		markAttention(deps, "user declined Wi-Fi bounce — "+h.Detail)
		return
	}

	st.attempted = true
	if deps.Health != nil {
		deps.Health.RecoveryStarted(HealthEntityID)
	}
	if err := deps.Bounce(ctx, wifi.Device); err != nil {
		slog.Error("hostwifi: bounce failed", "error", err)
		if deps.Health != nil {
			deps.Health.RecoveryFailed(HealthEntityID, err.Error())
		}
		return
	}

	time.Sleep(bounceSettleDelay)
	if !deps.AppChannel().StressPresent() {
		slog.Info("hostwifi: bounce restored app-channel reachability", "source", source)
		if deps.Health != nil {
			deps.Health.RecoverySucceeded(HealthEntityID)
		}
		st.consecutiveBad = 0
		clearEpisodeState(st)
		return
	}

	slog.Warn("hostwifi: bounce completed but app-channel still stale", "source", source)
	if deps.Health != nil {
		deps.Health.RecoveryFailed(HealthEntityID, "app-channel still stale after bounce")
	}
}

func clearEpisode(st *episodeState, deps Deps, reason string) {
	if st.inEpisode {
		slog.Debug("hostwifi: episode cleared", "reason", reason)
	}
	st.consecutiveBad = 0
	clearEpisodeState(st)
	if deps.Health != nil {
		deps.Health.Observe(HealthEntityID, true, reason)
	}
}

func clearEpisodeState(st *episodeState) {
	st.inEpisode = false
	st.attempted = false
	st.declined = false
}

func updateHealth(deps Deps, h Hypothesis, st *episodeState) {
	if deps.Health == nil {
		return
	}
	switch {
	case h.Solid:
		deps.Health.Observe(HealthEntityID, false, h.Detail)
	case st.consecutiveBad > 0:
		deps.Health.Observe(HealthEntityID, false, h.Detail)
	default:
		deps.Health.Observe(HealthEntityID, true, h.Detail)
	}
}

func markAttention(deps Deps, detail string) {
	if deps.Health != nil {
		deps.Health.MarkNeedsAttention(HealthEntityID, detail)
	}
}
