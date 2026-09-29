// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Host app for Spyder's alert runner. An XCUITest bundle needs a target
// app; the runner never interacts with this one.
import SwiftUI

@main
struct AlertHostApp: App {
    var body: some Scene {
        WindowGroup { Text("Spyder alert runner") }
    }
}
