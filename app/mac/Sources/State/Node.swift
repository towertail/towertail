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

    init(
        id: UUID = UUID(),
        displayName: String,
        kind: NodeKind,
        sshUser: String? = nil,
        sshHost: String? = nil,
        tags: [String] = [],
        enabled: Bool = true
    ) {
        self.id = id
        self.displayName = displayName
        self.kind = kind
        self.sshUser = sshUser
        self.sshHost = sshHost
        self.tags = tags
        self.enabled = enabled
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
