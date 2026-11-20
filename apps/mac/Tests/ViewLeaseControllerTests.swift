import XCTest
@testable import Towertail

/// A clock that the test steps. It stands in for `Task.sleep`.
private final class FakeClock: @unchecked Sendable {
    private struct Waiter {
        let id: UUID
        let deadline: Duration
        let continuation: CheckedContinuation<Void, Error>
    }

    private let lock = NSLock()
    private var now: Duration = .zero
    private var waiters: [Waiter] = []

    func sleep(_ d: Duration) async throws {
        let id = UUID()
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
                lock.lock()
                if Task.isCancelled {
                    lock.unlock()
                    c.resume(throwing: CancellationError())
                    return
                }
                waiters.append(Waiter(id: id, deadline: now + d, continuation: c))
                lock.unlock()
            }
        } onCancel: {
            let waiter: Waiter? = lock.withLock {
                guard let i = waiters.firstIndex(where: { $0.id == id }) else { return nil }
                return waiters.remove(at: i)
            }
            waiter?.continuation.resume(throwing: CancellationError())
        }
    }

    /// Moves the time forward. It wakes each sleeper at its deadline and lets
    /// the woken tasks run before it goes on.
    @MainActor
    func advance(_ d: Duration) async {
        // Let new tasks register their sleep first.
        for _ in 0..<20 { await Task.yield() }
        let target = lock.withLock { now + d }
        while true {
            let next: Waiter? = lock.withLock {
                guard let i = waiters.indices.filter({ waiters[$0].deadline <= target })
                    .min(by: { waiters[$0].deadline < waiters[$1].deadline }) else { return nil }
                let w = waiters.remove(at: i)
                now = max(now, w.deadline)
                return w
            }
            guard let next else { break }
            next.continuation.resume()
            for _ in 0..<20 { await Task.yield() }
        }
        lock.withLock { now = target }
    }
}

@MainActor
final class ViewLeaseControllerTests: XCTestCase {
    private var sent: [LiveView] = []
    private let clock = FakeClock()
    private let a = UUID(), b = UUID(), c = UUID()

    private func makeController() -> ViewLeaseController {
        ViewLeaseController(send: { [unowned self] in self.sent.append($0) },
                            sleep: { [clock] in try await clock.sleep($0) })
    }

    private func sorted(_ ids: UUID...) -> [UUID] { ids.sorted { $0.uuidString < $1.uuidString } }

    /// Popover visible with cards A and B on screen, and the debounce done.
    private func openPopover(_ lease: ViewLeaseController) async {
        lease.setPopover(visible: true)
        lease.setCard(a, visible: true)
        lease.setCard(b, visible: true)
        await clock.advance(.milliseconds(250))
    }

    func testPopoverShowSendsVisibleCards() async {
        let lease = makeController()
        await openPopover(lease)
        XCTAssertEqual(sent, [LiveView(hosts: sorted(a, b), detail: nil)])
    }

    func testHideSendsEmptyViewAtOnce() async {
        let lease = makeController()
        await openPopover(lease)
        lease.setPopover(visible: false)
        XCTAssertEqual(sent.count, 2)
        XCTAssertEqual(sent.last, .empty)
    }

    func testRenewsEvery30Seconds() async {
        let lease = makeController()
        await openPopover(lease)
        await clock.advance(.seconds(30))
        await clock.advance(.seconds(30))
        XCTAssertEqual(sent.count, 3)
        XCTAssertEqual(Set(sent.map { $0.hosts }).count, 1)
        XCTAssertEqual(sent.last, LiveView(hosts: sorted(a, b), detail: nil))
    }

    func testNoRenewWhileEmpty() async {
        let lease = makeController()
        lease.setPopover(visible: false)
        await clock.advance(.seconds(60))
        XCTAssertTrue(sent.isEmpty)
        await openPopover(lease)
        lease.setPopover(visible: false)
        let count = sent.count
        await clock.advance(.seconds(60))
        XCTAssertEqual(sent.count, count)
    }

    func testFullViewAddsDetail() async {
        let lease = makeController()
        lease.setFullView(window: UUID(), host: c, tab: .processes, visible: true)
        XCTAssertEqual(sent, [LiveView(hosts: [c], detail: ViewDetail(host: c, tab: .processes))])
    }

    func testDiskUsageTabHasNoDetail() async {
        let lease = makeController()
        lease.setFullView(window: UUID(), host: c, tab: nil, visible: true)
        XCTAssertEqual(sent, [LiveView(hosts: [c], detail: nil)])
    }

    func testFullViewAddsToPopoverHosts() async {
        let lease = makeController()
        await openPopover(lease)
        lease.setFullView(window: UUID(), host: c, tab: .health, visible: true)
        XCTAssertEqual(sent.last, LiveView(hosts: sorted(a, b, c), detail: ViewDetail(host: c, tab: .health)))
    }

    func testClosingFullViewEndsItsDetail() async {
        let lease = makeController()
        let window = UUID()
        lease.setFullView(window: window, host: c, tab: .ports, visible: true)
        lease.setFullView(window: window, host: c, tab: .ports, visible: false)
        XCTAssertEqual(sent.last, .empty)
    }

    func testSleepSendsEmptyAndWakeRestores() async {
        let lease = makeController()
        await openPopover(lease)
        lease.setSystemAwake(false)
        XCTAssertEqual(sent.last, .empty)
        lease.setSystemAwake(true)
        XCTAssertEqual(sent.last, LiveView(hosts: sorted(a, b), detail: nil))
        XCTAssertEqual(sent.count, 3)
    }

    func testLimitStopsRenewUntilChange() async {
        let lease = makeController()
        await openPopover(lease)
        lease.didReceiveLimit()
        await clock.advance(.seconds(60))
        XCTAssertEqual(sent.count, 1)
        lease.setCard(c, visible: true)
        await clock.advance(.milliseconds(250))
        XCTAssertEqual(sent.count, 2)
        XCTAssertEqual(sent.last?.hosts, sorted(a, b, c))
    }

    func testReconnectResendsView() async {
        let lease = makeController()
        await openPopover(lease)
        lease.streamDidConnect()
        XCTAssertEqual(sent.count, 2)
        XCTAssertEqual(sent.last, sent.first)
    }

    func testReconnectWithNothingVisibleSendsNothing() async {
        let lease = makeController()
        lease.streamDidConnect()
        XCTAssertTrue(sent.isEmpty)
    }

    func testCardScrollDebounce() async {
        let lease = makeController()
        lease.setPopover(visible: true)
        let ids = (0..<5).map { _ in UUID() }
        for id in ids {
            lease.setCard(id, visible: true)
            await clock.advance(.milliseconds(40))
        }
        XCTAssertTrue(sent.isEmpty)
        await clock.advance(.milliseconds(250))
        XCTAssertEqual(sent.count, 1)
        XCTAssertEqual(sent.first?.hosts.count, 5)
    }
}
