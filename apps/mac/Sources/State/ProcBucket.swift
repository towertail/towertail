import Foundation

/// The bucket of the process snapshot at one time. The age of the time
/// selects the tier, as the server does.
struct ProcBucket: Hashable, Sendable {
    enum Tier: String, Sendable {
        case raw
        case minute = "1m"
        case hour = "1h"
    }

    let tier: Tier
    let start: Date
    let span: TimeInterval
    let limit: Int

    var label: String {
        switch tier {
        case .raw: return "each sample (10 s)"
        case .minute: return "top 10 of each minute"
        case .hour: return "top 5 of each hour"
        }
    }

    /// Raw has no fixed interval on the server. 10 s is the usual one.
    static func of(_ t: Date, now: Date) -> ProcBucket {
        let age = now.timeIntervalSince(t)
        let (tier, span, limit): (Tier, TimeInterval, Int)
        if age <= 3_600 {
            (tier, span, limit) = (.raw, 10, 20)
        } else if age <= 7 * 86_400 {
            (tier, span, limit) = (.minute, 60, 10)
        } else {
            (tier, span, limit) = (.hour, 3_600, 5)
        }
        let s = (t.timeIntervalSince1970 / span).rounded(.down) * span
        return ProcBucket(tier: tier, start: Date(timeIntervalSince1970: s), span: span, limit: limit)
    }

    /// The bucket `n` spans away, with the same tier.
    func shifted(_ n: Int) -> ProcBucket {
        ProcBucket(tier: tier, start: start.addingTimeInterval(Double(n) * span), span: span, limit: limit)
    }
}
