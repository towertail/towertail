import Foundation
@testable import Towertail

/// A scanner that the test drives: each `scan` call hands its
/// continuation to the test.
final class FakeDiskScanner: DiskScanner, @unchecked Sendable {
    private let lock = NSLock()
    private var conts: [AsyncThrowingStream<DiskScanEvent, Error>.Continuation] = []
    private(set) var requests: [DiskScanRequest] = []
    private(set) var cancelled = 0

    func scan(node: Node, request: DiskScanRequest) -> AsyncThrowingStream<DiskScanEvent, Error> {
        AsyncThrowingStream { c in
            c.onTermination = { [weak self] reason in
                if case .cancelled = reason { self?.lock.withLock { self?.cancelled += 1 } }
            }
            lock.withLock {
                requests.append(request)
                conts.append(c)
            }
        }
    }

    var current: AsyncThrowingStream<DiskScanEvent, Error>.Continuation? { lock.withLock { conts.last } }
    var cancelCount: Int { lock.withLock { cancelled } }
    var requestList: [DiskScanRequest] { lock.withLock { requests } }

    func send(_ e: DiskScanEvent) { current?.yield(e) }
    func finish(throwing error: Error? = nil) {
        if let error { current?.finish(throwing: error) } else { current?.finish() }
    }
}
