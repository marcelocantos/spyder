// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI
import UIKit

@main
struct KeepAwakeApp: App {
    @Environment(\.scenePhase) private var scenePhase

    init() {
        // Enable battery monitoring so we get state-change notifications.
        // Without this, batteryState stays .unknown.
        UIDevice.current.isBatteryMonitoringEnabled = true

        // When the cable is pulled, there is nothing left to do — exit so
        // iOS reclaims the slot. The notification only fires on *changes*,
        // so a cold launch while already unplugged won't trigger it; we
        // handle that separately in the scene-phase observer below.
        NotificationCenter.default.addObserver(
            forName: UIDevice.batteryStateDidChangeNotification,
            object: nil,
            queue: .main
        ) { _ in
            if UIDevice.current.batteryState == .unplugged {
                exit(0)
            }
        }
    }

    var body: some Scene {
        WindowGroup {
            ContentView()
                .onChange(of: scenePhase) { _, phase in
                    UIApplication.shared.isIdleTimerDisabled = (phase == .active)
                    // If the app is foregrounded while unplugged (e.g. a
                    // Shortcut misfires, or the user launches manually),
                    // there's still no reason to hang around.
                    if phase == .active && UIDevice.current.batteryState == .unplugged {
                        exit(0)
                    }
                }
        }
    }
}
