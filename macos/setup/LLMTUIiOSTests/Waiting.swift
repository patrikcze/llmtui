import Foundation

/// Waits until `condition` holds, yielding so the app model's tasks can run,
/// and gives up after `timeout`. Bounded by time rather than by a number of
/// yields, so a slow CI simulator gets the same chance as a fast Mac.
@MainActor
func waitUntil(timeout: Duration = .seconds(30), _ condition: () -> Bool) async -> Bool {
    let clock = ContinuousClock()
    let deadline = clock.now.advanced(by: timeout)
    while !condition() {
        if clock.now >= deadline { return false }
        await Task.yield()
    }
    return true
}
