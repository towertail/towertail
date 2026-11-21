import Foundation

/// The ids of SSH hosts that a sign-out released while Cloud still held
/// them. The feed links these hosts again at the next sign-in.
struct ReleasedHosts {
    static let key = "cloud.releasedHosts"

    private let defaults: UserDefaults

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    var ids: Set<UUID> {
        Set((defaults.stringArray(forKey: Self.key) ?? []).compactMap(UUID.init(uuidString:)))
    }

    func add(_ new: some Sequence<UUID>) {
        save(ids.union(new))
    }

    /// Drops every id that is not in `keep`.
    func retain(_ keep: Set<UUID>) {
        let current = ids
        let kept = current.intersection(keep)
        if kept != current { save(kept) }
    }

    private func save(_ set: Set<UUID>) {
        defaults.set(set.map(\.uuidString).sorted(), forKey: Self.key)
    }
}
