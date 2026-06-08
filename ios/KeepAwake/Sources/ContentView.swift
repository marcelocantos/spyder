// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct ContentView: View {
    @Environment(\.scenePhase) private var scenePhase

    // Slow drift, "barely perceptible at seconds-scale, clearly moves
    // across the screen over several minutes". At iPad-class ~264 ppi,
    // 10 pt/s ≈ 1 mm/s. Different x/y speeds with a non-rational ratio
    // avoid a tight repeating diagonal so coverage fills the screen.
    private let speedX: Double = 0.5
    private let speedY: Double = 0.35

    var body: some View {
        GeometryReader { proxy in
            let size = proxy.size
            ZStack {
                Color.black.ignoresSafeArea()

                // 1 Hz tick — at 1 pt/s the position only changes by one
                // point per second, so there is nothing to render between
                // ticks. Cuts the per-frame SwiftUI re-evaluation cost ~60×.
                TimelineView(.periodic(from: .now, by: 1.0)) { timeline in
                    let t = timeline.date.timeIntervalSinceReferenceDate
                    let contentWidth = min(size.width, 280)
                    let contentHeight = min(size.height, 260)
                    let xRange = max(0, size.width - contentWidth)
                    let yRange = max(0, size.height - contentHeight)
                    let x = contentWidth / 2 + triangle(t * speedX, range: xRange)
                    let y = contentHeight / 2 + triangle(t * speedY, range: yRange)
                    content
                        .frame(width: contentWidth, height: contentHeight)
                        .position(x: x, y: y)
                }
            }
        }
        .preferredColorScheme(.dark)
        .ignoresSafeArea()
    }

    private var content: some View {
        VStack(spacing: 24) {
            Image(systemName: "bolt.fill")
                .font(.system(size: 96))
                .foregroundStyle(scenePhase == .active ? Color.yellow.opacity(0.5) : .secondary)

            Text("KeepAwake")
                .font(.largeTitle.bold())
                .foregroundStyle(.white)

            Text(scenePhase == .active
                 ? "Screen stays on while this app is foregrounded."
                 : "Bring the app to the foreground to keep the screen awake.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.white.opacity(0.6))
                .padding(.horizontal, 40)
        }
    }

    // Reflective bounce: position oscillates over [0, range] with period
    // 2·range, traced by a triangle wave. Stateless — no accumulating
    // drift across pause/resume.
    private func triangle(_ t: Double, range: Double) -> CGFloat {
        guard range > 0 else { return 0 }
        let period = 2 * range
        let mod = t.truncatingRemainder(dividingBy: period)
        let phase = mod < 0 ? mod + period : mod
        return CGFloat(phase < range ? phase : period - phase)
    }
}

#Preview {
    ContentView()
}
