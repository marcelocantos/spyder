// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct ContentView: View {
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        VStack(spacing: 24) {
            Image(systemName: "bolt.fill")
                .font(.system(size: 96))
                .foregroundStyle(scenePhase == .active ? .yellow : .secondary)
                .symbolEffect(.pulse, options: .repeat(.continuous), isActive: scenePhase == .active)

            Text("KeepAwake")
                .font(.largeTitle.bold())

            Text(scenePhase == .active
                 ? "Screen stays on while this app is foregrounded."
                 : "Bring the app to the foreground to keep the screen awake.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 40)
        }
    }
}

#Preview {
    ContentView()
}
