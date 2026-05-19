// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI
import os

// BouncingBall is a test fixture for spyder's log-capture path: it
// emits a recognisable `os_log` entry on every wall bounce, several
// per second, indefinitely. Unlike KeepAwake (which only logs scene-
// phase transitions and is otherwise silent), BouncingBall produces
// a steady stream of third-party-app emissions — exactly the
// signal spyder's DTX activitytracetap path is supposed to surface.
//
// Subsystem: com.marcelocantos.spyder.BouncingBall
// Category:  physics
// Format:    "bounce wall=<top|bottom|left|right> count=<n> x=<f> y=<f>"
private let log = Logger(
    subsystem: "com.marcelocantos.spyder.BouncingBall",
    category: "physics"
)

struct ContentView: View {
    @State private var position = CGPoint(x: 100, y: 100)
    @State private var velocity = CGSize(width: 220, height: 170) // points/sec
    @State private var bounceCount = 0
    @State private var lastTick: Date?

    private let ballRadius: CGFloat = 24

    var body: some View {
        GeometryReader { proxy in
            ZStack {
                Color.black.ignoresSafeArea()

                TimelineView(.animation(minimumInterval: 1.0 / 60.0)) { context in
                    Circle()
                        .fill(Color.yellow)
                        .frame(width: ballRadius * 2, height: ballRadius * 2)
                        .position(position)
                        .onChange(of: context.date) { _, now in
                            advance(to: now, in: proxy.size)
                        }
                }

                VStack {
                    Text("BouncingBall · bounces: \(bounceCount)")
                        .font(.system(.headline, design: .monospaced))
                        .foregroundStyle(.white.opacity(0.6))
                        .padding(.top, 20)
                    Spacer()
                }
            }
        }
        .preferredColorScheme(.dark)
        .ignoresSafeArea()
        .onAppear {
            UIApplication.shared.isIdleTimerDisabled = true
            log.info("scene appear — initial position=\(position.x, privacy: .public),\(position.y, privacy: .public)")
        }
    }

    private func advance(to now: Date, in size: CGSize) {
        let last = lastTick ?? now
        let dt = CGFloat(now.timeIntervalSince(last))
        lastTick = now
        guard dt > 0 && dt < 0.25 else { return }

        var x = position.x + velocity.width * dt
        var y = position.y + velocity.height * dt

        // Wall collisions: reflect velocity, clamp position, log.
        let minX = ballRadius, maxX = size.width - ballRadius
        let minY = ballRadius, maxY = size.height - ballRadius
        if x < minX {
            x = minX
            velocity.width = abs(velocity.width)
            bounce(wall: "left", x: x, y: y)
        } else if x > maxX {
            x = maxX
            velocity.width = -abs(velocity.width)
            bounce(wall: "right", x: x, y: y)
        }
        if y < minY {
            y = minY
            velocity.height = abs(velocity.height)
            bounce(wall: "top", x: x, y: y)
        } else if y > maxY {
            y = maxY
            velocity.height = -abs(velocity.height)
            bounce(wall: "bottom", x: x, y: y)
        }

        position = CGPoint(x: x, y: y)
    }

    private func bounce(wall: String, x: CGFloat, y: CGFloat) {
        bounceCount += 1
        log.info("bounce wall=\(wall, privacy: .public) count=\(bounceCount, privacy: .public) x=\(x, privacy: .public) y=\(y, privacy: .public)")
    }
}

#Preview {
    ContentView()
}
