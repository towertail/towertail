import SwiftUI

struct CardChrome<Content: View>: View {
    let tint: ThresholdTint
    let offline: Bool
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            content()
        }
        .padding(12)
        .background(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .fill(Color("Card/Background"))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .strokeBorder(borderColor, lineWidth: borderWidth)
        )
    }

    private var borderColor: Color {
        if offline { return Color.secondary.opacity(0.25) }
        switch tint {
        case .warn: return ThresholdTint.warn.color
        case .critical: return ThresholdTint.critical.color
        case .nominal, .stale: return Color.secondary.opacity(0.18)
        }
    }

    private var borderWidth: CGFloat {
        switch tint {
        case .warn, .critical: return 1.5
        default: return 0.5
        }
    }
}
