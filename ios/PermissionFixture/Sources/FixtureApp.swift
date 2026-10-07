// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Network
import SwiftUI

// Keep a real Bonjour browser alive while the app is foregrounded. Starting
// the browse raises iOS's Local Network permission sheet on a fresh install;
// this fixture neither connects to nor transmits data to discovered hosts.
@MainActor
final class PermissionProbe: ObservableObject {
    private var browser: NWBrowser?

    func start() {
        guard browser == nil else { return }
        let browser = NWBrowser(for: .bonjour(type: "_spyderfixture._tcp", domain: nil), using: .tcp)
        browser.stateUpdateHandler = { _ in }
        browser.start(queue: .main)
        self.browser = browser
    }
}

@main
struct FixtureApp: App {
    @StateObject private var probe = PermissionProbe()

    var body: some Scene {
        WindowGroup {
            Text("Disposable Local Network permission fixture")
                .onAppear { probe.start() }
        }
    }
}
