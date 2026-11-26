import XCTest

/// Polls `cond` every 10 ms. Fails the test at the caller's line after `timeout` seconds.
@MainActor
func waitFor(timeout: TimeInterval = 5,
             file: StaticString = #filePath,
             line: UInt = #line,
             _ cond: @MainActor () -> Bool) async throws {
    let end = Date().addingTimeInterval(timeout)
    while !cond() {
        if Date() > end {
            return XCTFail("condition not met in \(timeout) s", file: file, line: line)
        }
        try await Task.sleep(nanoseconds: 10_000_000)
    }
}
