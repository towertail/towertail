import Foundation

enum NodeKind: String, Codable, Sendable, CaseIterable {
    case local
    case ssh
}

/// How we authenticate to an SSH host. "Key" covers both on-disk private
/// keys in ~/.ssh and ssh-agent / Pageant; "Password" pulls the plaintext
/// from Keychain (Mac) or DPAPI (Windows) at connect time.
enum AuthMethod: String, Codable, Sendable, CaseIterable {
    case key
    case password
}

struct Node: Codable, Identifiable, Equatable, Sendable {
    var id: UUID
    var displayName: String
    var kind: NodeKind
    var sshUser: String?
    var sshHost: String?
    /// SSH port; nil → default 22. Stored as optional so existing records
    /// round-trip unchanged without stamping an implicit 22 everywhere.
    var sshPort: Int?
    /// How to authenticate. Defaults to .key for back-compat — existing
    /// records with no field on disk keep their current (key-only) behavior.
    var authMethod: AuthMethod
    /// SHA256-base64 fingerprint of the remote host key we trust for this
    /// node. nil → no key pinned yet; first connect prompts the user.
    /// Mismatch at connect time refuses rather than silently accepting.
    var knownHostFingerprint: String?
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
    // thresholds from ServerSettings. Kept as a single optional (not a set of
    // booleans + doubles) so "disabled" and "never set" look the same on
    // disk — reverting to global is just clearing the field.
    var customThresholds: MetricThresholds?
    /// If set to a future date, notifications for this node are suppressed
    /// until that time. Past dates are ignored (the notifier does a <=
    /// check), so we don't have to actively clear expired snoozes — they
    /// fall off naturally as time passes.
    var snoozedUntil: Date?
    /// User-pinned favorite. Favorites sort to the top of the popover list.
    var favorite: Bool
    /// Timestamp of the most recent successful sample. Persisted so a host
    /// that worked yesterday but is unreachable today still escalates the
    /// menu-bar icon to red on relaunch — without this, every restart
    /// would reset the "ever connected" memory and treat known-good hosts
    /// like brand-new ones.
    var lastSuccessfulConnect: Date?

    init(
        id: UUID = UUID(),
        displayName: String,
        kind: NodeKind,
        sshUser: String? = nil,
        sshHost: String? = nil,
        sshPort: Int? = nil,
        authMethod: AuthMethod = .key,
        knownHostFingerprint: String? = nil,
        tags: [String] = [],
        enabled: Bool = true,
        iconOnWarn: Bool = true,
        iconOnCritical: Bool = true,
        notifyOnWarn: Bool = true,
        notifyOnCritical: Bool = true,
        customThresholds: MetricThresholds? = nil,
        snoozedUntil: Date? = nil,
        favorite: Bool = false,
        lastSuccessfulConnect: Date? = nil
    ) {
        self.id = id
        self.displayName = displayName
        self.kind = kind
        self.sshUser = sshUser
        self.sshHost = sshHost
        self.sshPort = sshPort
        self.authMethod = authMethod
        self.knownHostFingerprint = knownHostFingerprint
        self.tags = tags
        self.enabled = enabled
        self.iconOnWarn = iconOnWarn
        self.iconOnCritical = iconOnCritical
        self.notifyOnWarn = notifyOnWarn
        self.notifyOnCritical = notifyOnCritical
        self.customThresholds = customThresholds
        self.snoozedUntil = snoozedUntil
        self.favorite = favorite
        self.lastSuccessfulConnect = lastSuccessfulConnect
    }

    enum CodingKeys: String, CodingKey {
        case id, displayName, kind, sshUser, sshHost, sshPort, authMethod
        case knownHostFingerprint
        case tags, enabled
        case iconOnWarn, iconOnCritical, notifyOnWarn, notifyOnCritical
        case customThresholds, snoozedUntil, favorite
        case lastSuccessfulConnect
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
        try c.encodeIfPresent(sshPort, forKey: .sshPort)
        try c.encode(authMethod, forKey: .authMethod)
        try c.encodeIfPresent(knownHostFingerprint, forKey: .knownHostFingerprint)
        try c.encode(tags, forKey: .tags)
        try c.encode(enabled, forKey: .enabled)
        try c.encode(iconOnWarn, forKey: .iconOnWarn)
        try c.encode(iconOnCritical, forKey: .iconOnCritical)
        try c.encode(notifyOnWarn, forKey: .notifyOnWarn)
        try c.encode(notifyOnCritical, forKey: .notifyOnCritical)
        try c.encodeIfPresent(customThresholds, forKey: .customThresholds)
        try c.encodeIfPresent(snoozedUntil, forKey: .snoozedUntil)
        try c.encode(favorite, forKey: .favorite)
        try c.encodeIfPresent(lastSuccessfulConnect, forKey: .lastSuccessfulConnect)
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.id = try c.decode(UUID.self, forKey: .id)
        self.displayName = try c.decode(String.self, forKey: .displayName)
        self.kind = try c.decode(NodeKind.self, forKey: .kind)
        self.sshUser = try c.decodeIfPresent(String.self, forKey: .sshUser)
        self.sshHost = try c.decodeIfPresent(String.self, forKey: .sshHost)
        self.sshPort = try c.decodeIfPresent(Int.self, forKey: .sshPort)
        // Default .key so old records (no authMethod on disk) keep their
        // existing key-only behavior and nobody suddenly gets prompted.
        self.authMethod = try c.decodeIfPresent(AuthMethod.self, forKey: .authMethod) ?? .key
        self.knownHostFingerprint = try c.decodeIfPresent(String.self, forKey: .knownHostFingerprint)
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
        self.favorite = try c.decodeIfPresent(Bool.self, forKey: .favorite) ?? false
        self.lastSuccessfulConnect = try c.decodeIfPresent(Date.self, forKey: .lastSuccessfulConnect)
    }

    /// Equality deliberately ignores `lastSuccessfulConnect`. The collector
    /// supervisor uses `currentNode != entry.node` to decide whether to
    /// respawn a pacer (e.g. after the user changes the auth method);
    /// stamping `lastSuccessfulConnect` on every fresh install would
    /// otherwise cancel and restart the pacer the moment it succeeded
    /// for the first time. Field is also one we never want a user-driven
    /// "Reset" UI to surface as a diff — it's an internal liveness flag.
    static func == (lhs: Node, rhs: Node) -> Bool {
        lhs.id == rhs.id
            && lhs.displayName == rhs.displayName
            && lhs.kind == rhs.kind
            && lhs.sshUser == rhs.sshUser
            && lhs.sshHost == rhs.sshHost
            && lhs.sshPort == rhs.sshPort
            && lhs.authMethod == rhs.authMethod
            && lhs.knownHostFingerprint == rhs.knownHostFingerprint
            && lhs.tags == rhs.tags
            && lhs.enabled == rhs.enabled
            && lhs.iconOnWarn == rhs.iconOnWarn
            && lhs.iconOnCritical == rhs.iconOnCritical
            && lhs.notifyOnWarn == rhs.notifyOnWarn
            && lhs.notifyOnCritical == rhs.notifyOnCritical
            && lhs.customThresholds == rhs.customThresholds
            && lhs.snoozedUntil == rhs.snoozedUntil
            && lhs.favorite == rhs.favorite
    }

    /// True only when snoozedUntil is set and still in the future.
    var isSnoozed: Bool {
        guard let until = snoozedUntil else { return false }
        return until > Date()
    }

    /// Effective port (sshPort ?? 22). Used by the SSH factory and for UI.
    var effectiveSshPort: Int { sshPort ?? 22 }

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
