import XCTest

final class MCPServerUITests: XCTestCase {
    @MainActor func testServerEditorValidationAndCancel() {
        let app = XCUIApplication()
        app.launch()
        app.tabBars.buttons["Settings"].tap()
        app.buttons["MCP Servers"].tap()
        app.buttons["mcp.add"].tap()
        XCTAssertTrue(app.textFields["mcp.name"].waitForExistence(timeout: 5))
        let endpoint = app.textFields["mcp.endpoint"]
        endpoint.tap()
        endpoint.press(forDuration: 1.2)
        if app.menuItems["Select All"].exists { app.menuItems["Select All"].tap() }
        // Clear using the field's existing text without assuming its length.
        let current = endpoint.value as? String ?? ""
        endpoint.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: current.count) + "not-a-url")
        app.buttons["mcp.save"].tap()
        XCTAssertTrue(app.staticTexts["Enter an HTTP or HTTPS MCP endpoint without embedded credentials."].waitForExistence(timeout: 5))
        app.buttons["Cancel"].tap()
        XCTAssertTrue(app.buttons["mcp.add"].waitForExistence(timeout: 5))
    }
}
