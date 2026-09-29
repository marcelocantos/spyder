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
}
