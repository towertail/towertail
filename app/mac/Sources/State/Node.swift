import Foundation

enum NodeKind: String, Codable, Sendable, CaseIterable {
    case local
    case ssh
}

struct Node: Codable, Identifiable, Equatable, Sendable {
    var id: UUID
    var displayName: String
    var kind: NodeKind
    var sshUser: String?
    var sshHost: String?
    var tags: [String]
    var enabled: Bool
    // Per-level menu-bar contribution: lets the user say "yellow when vm
    // complains, but only go red for production". Each flag gates its
    // corresponding severity independently of notifications.
    var iconOnWarn: Bool
    var iconOnCritical: Bool
    var notifyOnWarn: Bool
    var notifyOnCritical: Bool
    // Per-node threshold override. When nil the server uses the global
    // thresholds from AppSettings. Kept as a single optional (not a set of
    // booleans + doubles) so "disabled" and "never set" look the same on
    // disk — reverting to global is just clearing the field.
    var customThresholds: MetricThresholds?
    /// If set to a future date, notifications for this node are suppressed
    /// until that time. Past dates are ignored (the notifier does a <=
    /// check), so we don't have to actively clear expired snoozes — they
    /// fall off naturally as time passes.
    var snoozedUntil: Date?

    init(
        id: UUID = UUID(),
        displayName: String,
        kind: NodeKind,
        sshUser: String? = nil,
        sshHost: String? = nil,
        tags: [String] = [],
        enabled: Bool = true,
        iconOnWarn: Bool = true,
        iconOnCritical: Bool = true,
        notifyOnWarn: Bool = true,
        notifyOnCritical: Bool = true,
        customThresholds: MetricThresholds? = nil,
        snoozedUntil: Date? = nil
    ) {
        self.id = id
        self.displayName = displayName
        self.kind = kind
        self.sshUser = sshUser
        self.sshHost = sshHost
        self.tags = tags
        self.enabled = enabled
        self.iconOnWarn = iconOnWarn
        self.iconOnCritical = iconOnCritical
        self.notifyOnWarn = notifyOnWarn
        self.notifyOnCritical = notifyOnCritical
        self.customThresholds = customThresholds
        self.snoozedUntil = snoozedUntil
    }

    enum CodingKeys: String, CodingKey {
        case id, displayName, kind, sshUser, sshHost, tags, enabled
        case iconOnWarn, iconOnCritical, notifyOnWarn, notifyOnCritical
        case customThresholds, snoozedUntil
        // Legacy single-toggle flag from the first pass. If present it
        // seeds both iconOnWarn and iconOnCritical so users who already
        // disabled menu-bar icon for a noisy host keep that behavior after
        // the per-level split.
        case contributesToMenuBarIcon
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(displayName, forKey: .displayName)
        try c.encode(kind, forKey: .kind)
        try c.encodeIfPresent(sshUser, forKey: .sshUser)
        try c.encodeIfPresent(sshHost, forKey: .sshHost)
        try c.encode(tags, forKey: .tags)
        try c.encode(enabled, forKey: .enabled)
        try c.encode(iconOnWarn, forKey: .iconOnWarn)
        try c.encode(iconOnCritical, forKey: .iconOnCritical)
        try c.encode(notifyOnWarn, forKey: .notifyOnWarn)
        try c.encode(notifyOnCritical, forKey: .notifyOnCritical)
        try c.encodeIfPresent(customThresholds, forKey: .customThresholds)
        try c.encodeIfPresent(snoozedUntil, forKey: .snoozedUntil)
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.id = try c.decode(UUID.self, forKey: .id)
        self.displayName = try c.decode(String.self, forKey: .displayName)
        self.kind = try c.decode(NodeKind.self, forKey: .kind)
        self.sshUser = try c.decodeIfPresent(String.self, forKey: .sshUser)
        self.sshHost = try c.decodeIfPresent(String.self, forKey: .sshHost)
        self.tags = try c.decodeIfPresent([String].self, forKey: .tags) ?? []
        self.enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? true

        // Back-compat: prefer the new per-level fields; fall back to the
        // legacy single toggle; finally default to true so upgrades match
        // the pre-feature "everything lights up the icon" behavior.
        let legacy = try c.decodeIfPresent(Bool.self, forKey: .contributesToMenuBarIcon)
        self.iconOnWarn = try c.decodeIfPresent(Bool.self, forKey: .iconOnWarn) ?? legacy ?? true
        self.iconOnCritical = try c.decodeIfPresent(Bool.self, forKey: .iconOnCritical) ?? legacy ?? true
        self.notifyOnWarn = try c.decodeIfPresent(Bool.self, forKey: .notifyOnWarn) ?? true
        self.notifyOnCritical = try c.decodeIfPresent(Bool.self, forKey: .notifyOnCritical) ?? true
        self.customThresholds = try c.decodeIfPresent(MetricThresholds.self, forKey: .customThresholds)
        self.snoozedUntil = try c.decodeIfPresent(Date.self, forKey: .snoozedUntil)
    }

    /// True only when snoozedUntil is set and still in the future.
    var isSnoozed: Bool {
        guard let until = snoozedUntil else { return false }
        return until > Date()
    }

    static func localMac(displayName: String = "This Mac") -> Node {
        Node(displayName: displayName, kind: .local)
    }

    var userAtHost: String {
        switch kind {
        case .local:
            return "local"
        case .ssh:
            let user = sshUser?.isEmpty == false ? sshUser! : "?"
            let host = sshHost?.isEmpty == false ? sshHost! : "?"
            return "\(user)@\(host)"
        }
    }
}
