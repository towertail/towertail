import Foundation

enum ByteFormat {
    /// "1023 B", "1 KB", "1.5 MB", "3.00 GB". Negative values give "0 B".
    nonisolated static func size(_ bytes: Int64) -> String {
        let v = Double(max(bytes, 0))
        if v < 1024 { return String(format: "%.0f B", v) }
        if v < 1_048_576 { return String(format: "%.0f KB", v / 1024) }
        if v < 1_073_741_824 { return String(format: "%.1f MB", v / 1_048_576) }
        return String(format: "%.2f GB", v / 1_073_741_824)
    }

    /// Adaptive unit, so small values stay readable. 500 B/s stays in bytes,
    /// 120_000 B/s gives "117.2 KB/s".
    nonisolated static func rate(bytesPerSec: Double) -> String {
        let v = max(0, bytesPerSec)
        if v < 1_024 { return String(format: "%.0f B/s", v) }
        if v < 1_048_576 { return String(format: "%.1f KB/s", v / 1_024) }
        if v < 1_073_741_824 { return String(format: "%.1f MB/s", v / 1_048_576) }
        return String(format: "%.2f GB/s", v / 1_073_741_824)
    }
}
