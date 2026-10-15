import Foundation

/// Field projection for `logSettingsDiff` — pairs a log key with a closure
/// that pulls the rendered value out of the persisted struct. The closure
/// returns `String` so different field types (Bool, Int, Double, String,
/// nested struct) can format themselves uniformly.
struct SettingsField<T> {
    let key: String
    let value: (T) -> String

    init(_ key: String, _ value: @escaping (T) -> String) {
        self.key = key
        self.value = value
    }
}

/// Compares `before` and `after` field-by-field via `fields`. Any field
/// whose rendered value differs is logged as `key=before→after` under the
/// "settings" category. Returning early on no-changes keeps the log
/// readable when persist() runs without user edits (e.g. on launch).
@MainActor
func logSettingsDiff<T>(
    before: T,
    after: T,
    fields: [SettingsField<T>]
) {
    var changes: [String: String] = [:]
    for f in fields {
        let b = f.value(before)
        let a = f.value(after)
        if b != a {
            changes[f.key] = "\(b)→\(a)"
        }
    }
    if changes.isEmpty { return }
    Logger.shared.info("settings: changed", category: "settings", kv: changes)
}
