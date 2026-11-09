import Foundation

/// Compact duration text for cards and notifications.
enum ShortDuration {
    /// "30s", "2m", "68h". Hours do not roll into days, the same as the card.
    static func text(_ seconds: TimeInterval) -> String {
        let s = Int(max(0, seconds))
        if s < 60 { return "\(s)s" }
        if s < 3600 { return "\(s / 60)m" }
        return "\(s / 3600)h"
    }

    /// "45m", "3h 10m", "2d 4h".
    static func long(_ seconds: TimeInterval) -> String {
        let s = Int(max(0, seconds))
        if s < 3600 { return text(seconds) }
        if s < 86_400 {
            let m = (s % 3600) / 60
            return m == 0 ? "\(s / 3600)h" : "\(s / 3600)h \(m)m"
        }
        let h = (s % 86_400) / 3600
        return h == 0 ? "\(s / 86_400)d" : "\(s / 86_400)d \(h)h"
    }

    /// "41m", "5h", "3d": the age of a process.
    static func age(_ seconds: TimeInterval) -> String {
        seconds < 86_400 ? text(seconds) : "\(Int(seconds) / 86_400)d"
    }
}
