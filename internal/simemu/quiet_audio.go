// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package simemu

import "log/slog"

// callServicesJob is the iOS simulator's call-services daemon. In a
// headless simulator it opens a simulator audio device on the host's
// default output and keeps it running, silent, for as long as the
// simulator is up. It runs at background priority, so it misses the
// output's IO deadline several times a second. On the built-in speakers
// that is ~100 CoreAudio overloads per 30 s (cause
// ClientProcessIsThrottled), and every other app playing through the
// same device crackles (🎯T241).
const callServicesJob = "com.apple.telephonyutilities.callservicesd"

// QuietSimAudio disables and stops callservicesd in a booted simulator.
// The disable is stored in the simulator's own data, so later boots of
// the same simulator never start it. Failures are logged, not returned:
// a runtime without the job still boots and works.
//
// `simctl boot --disabledJob=` is not used because it has no effect on
// this job: with iOS 18.6, callservicesd came up and opened the audio
// device anyway.
func QuietSimAudio(udid string) {
	quietSimAudio(udid, func(args ...string) error {
		_, err := runCapture("xcrun", append([]string{"simctl"}, args...)...)
		return err
	})
}

func quietSimAudio(udid string, simctl func(args ...string) error) {
	target := "system/" + callServicesJob
	if err := simctl("spawn", udid, "launchctl", "disable", target); err != nil {
		slog.Warn("simemu: could not disable the simulator's callservicesd; host audio may crackle", "udid", udid, "err", err)
		return
	}
	// bootout fails when the job is not loaded, which is the state we want
	// (a simulator disabled on an earlier boot never loads it).
	if err := simctl("spawn", udid, "launchctl", "bootout", target); err != nil {
		slog.Debug("simemu: callservicesd was not running", "udid", udid, "err", err)
	}
}
