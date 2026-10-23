// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import XCTest

// Taps a button on an OS-owned alert. Spyder passes, as environment:
//   SPYDER_ALERT_BUTTON    the button label, e.g. "Allow"
//   SPYDER_ALERT_WAIT_SEC  how long to wait for the button (default 10)
final class SystemAlertTests: XCTestCase {
    func testTapSystemAlertButton() throws {
        let env = ProcessInfo.processInfo.environment
        let label = try XCTUnwrap(env["SPYDER_ALERT_BUTTON"], "SPYDER_ALERT_BUTTON is required")
        let wait = TimeInterval(env["SPYDER_ALERT_WAIT_SEC"] ?? "") ?? 10
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let button = springboard.buttons[label]
        XCTAssertTrue(button.waitForExistence(timeout: wait), "no system alert button \"\(label)\"")
        button.tap()
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: button)
        XCTAssertEqual(XCTWaiter.wait(for: [gone], timeout: 5), .completed,
                       "system alert button \"\(label)\" still showing after the tap")
    }

    // testHoldAgent is the resident automation session. It stays in this
    // method so Spyder can reuse one runner process across clients. A
    // command file in Documents names a SpringBoard button to tap; the
    // one-shot test is unchanged for a runner that is not already up.
    // It has no lifetime of its own (🎯T175): the daemon ends the session
    // when it stops or replaces the runner.
    func testHoldAgent() {
        // Only applies when test timeouts are on; keep it out of the way.
        executionTimeAllowance = 10 * 365 * 24 * 3600
        let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let cmdURL = docs.appendingPathComponent("spyder-agent-cmd.txt")
        let resultURL = docs.appendingPathComponent("spyder-agent-result.txt")
        while true {
            if let raw = try? String(contentsOf: cmdURL, encoding: .utf8) {
                try? FileManager.default.removeItem(at: cmdURL)
                let label = raw.trimmingCharacters(in: .whitespacesAndNewlines)
                if label != "" {
                    let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
                    let button = springboard.buttons[label]
                    if button.waitForExistence(timeout: 5) {
                        button.tap()
                        try? "tapped\n".write(to: resultURL, atomically: true, encoding: .utf8)
                    } else {
                        try? "missing\n".write(to: resultURL, atomically: true, encoding: .utf8)
                    }
                }
            }
            Thread.sleep(forTimeInterval: 0.4)
        }
    }
}
