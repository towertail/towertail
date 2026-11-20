import SwiftUI

/// The small tag after the host name, on a card and on a row.
enum HostTag: Equatable {
    case local
    case cloud
    case cloudOffline
    case sshOnly
    case offlineAlerted

    /// Nil when no tag shows. An SSH host gets a tag only when the user is
    /// signed in: with no account, each host is SSH, so the tag tells nothing.
    static func tag(for node: Node, signedIn: Bool, feedConnected: Bool, alerted: Bool) -> HostTag? {
        if node.kind == .local { return .local }
        if node.isCloud {
            if alerted { return .offlineAlerted }
            return feedConnected ? .cloud : .cloudOffline
        }
        return signedIn ? .sshOnly : nil
    }

    @MainActor
    static func tag(forNode id: UUID, in nodeStore: NodeStore, cloud: CloudSession?) -> HostTag? {
        guard let node = nodeStore.node(withId: id) else { return nil }
        return tag(
            for: node,
            signedIn: cloud?.isSignedIn ?? false,
            feedConnected: cloud?.feed?.connected ?? false,
            alerted: cloud?.feed?.alerted.contains(id) ?? false
        )
    }

    var label: String {
        switch self {
        case .local: return "LOCAL"
        case .cloud: return "CLOUD"
        case .cloudOffline: return "CLOUD OFFLINE"
        case .sshOnly: return "SSH ONLY"
        case .offlineAlerted: return "OFFLINE · ALERTED"
        }
    }

    var color: Color {
        switch self {
        case .cloud: return .accentColor
        case .offlineAlerted: return ThresholdTint.critical.color
        case .local, .cloudOffline, .sshOnly: return .secondary
        }
    }
}

struct HostTagView: View {
    let tag: HostTag

    var body: some View {
        Text(tag.label)
            .font(Typography.rowTag)
            .tracking(0.5)
            .foregroundStyle(tag.color)
            .lineLimit(1)
            .fixedSize()
            .padding(.horizontal, 5)
            .padding(.vertical, 1.5)
            .background(Capsule().fill(tag.color.opacity(0.14)))
            .accessibilityLabel(tag.label.capitalized)
    }
}
