import SwiftUI
import AppKit

extension Color {
    /// A color that follows the appearance of the window.
    init(light: UInt32, dark: UInt32) {
        self.init(nsColor: NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            let v = isDark ? dark : light
            return NSColor(
                srgbRed: CGFloat((v >> 16) & 0xff) / 255,
                green: CGFloat((v >> 8) & 0xff) / 255,
                blue: CGFloat(v & 0xff) / 255,
                alpha: 1
            )
        })
    }
}

/// Colors of the host workbench. The values follow the web workbench.
enum WB {
    static let bg = Color(light: 0xf6f6f8, dark: 0x121214)
    static let sidebar = Color(light: 0xefeff2, dark: 0x161618)
    static let surface = Color(light: 0xffffff, dark: 0x1c1c1e)
    static let hover = Color(light: 0xebebef, dark: 0x232326)
    static let border = Color(light: 0xdcdce2, dark: 0x2e2e33)
    static let muted = Color(light: 0x5c5c66, dark: 0x9a9aa2)
    static let faint = Color(light: 0x6b6b73, dark: 0x8e8e96)
    static let pin = Color(light: 0x9a7b00, dark: 0xffd60a)
    static let info = Color(light: 0x0a6fd8, dark: 0x0a84ff)

    static let cpu = Color(light: 0x1f9d48, dark: 0x30d158)
    static let mem = Color(light: 0x0a6fd8, dark: 0x0a84ff)
    static let load = Color(light: 0x5b5bd6, dark: 0x7d7dff)
    static let disk = Color(light: 0x0e8a8a, dark: 0x40c8c8)
    static let netIn = Color(light: 0x0a6fd8, dark: 0x0a84ff)
    static let netOut = Color(light: 0x8e44c9, dark: 0xbf5af2)
    static let read = Color(light: 0x0e8a8a, dark: 0x40c8c8)
    static let write = Color(light: 0xc0397a, dark: 0xff5ca8)
    static let health = Color(light: 0x0b7fb0, dark: 0x64d2ff)

    static let mono = Font.system(size: 11, weight: .medium, design: .monospaced)
}

extension ThresholdTint {
    /// Text color of a value. A nominal value uses the normal text color.
    var valueColor: Color { self == .nominal ? .primary : color }
}

extension View {
    /// The card of the workbench: surface, border, 12 pt corners.
    func wbCard(fill: Color = WB.surface) -> some View {
        background(fill, in: RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).stroke(WB.border, lineWidth: 1))
    }
}

/// A small uppercase label in a mono font.
struct KLabel: View {
    let text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text.uppercased())
            .font(.system(size: 10.5, weight: .medium, design: .monospaced))
            .tracking(1.1)
            .foregroundStyle(WB.faint)
    }
}

/// Rounded capsule with a mono text, for the live and pin chips.
struct WBChip: View {
    let text: String
    let color: Color

    var body: some View {
        Text(text)
            .font(.system(size: 11, weight: .medium, design: .monospaced))
            .foregroundStyle(color)
            .padding(.horizontal, 8).padding(.vertical, 2)
            .overlay(Capsule().stroke(color.opacity(0.8), lineWidth: 1))
    }
}

/// Segmented buttons in the look of the workbench.
struct WBSegments<T: Hashable & Identifiable>: View {
    let items: [T]
    @Binding var selection: T
    let label: (T) -> String
    var isEnabled: (T) -> Bool = { _ in true }
    var uppercase = false

    var body: some View {
        HStack(spacing: 2) {
            ForEach(items) { item in
                let on = item == selection
                Button {
                    selection = item
                } label: {
                    Text(uppercase ? label(item).uppercased() : label(item))
                        .font(.system(size: 12, weight: .medium, design: .monospaced))
                        .tracking(uppercase ? 0.7 : 0)
                        .foregroundStyle(on ? Color.primary : WB.muted)
                        .padding(.horizontal, uppercase ? 12 : 10).padding(.vertical, 4)
                        .background(on ? WB.border : .clear, in: RoundedRectangle(cornerRadius: 6))
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(!isEnabled(item))
                .opacity(isEnabled(item) ? 1 : 0.35)
            }
        }
        .padding(3)
        .background(WB.surface, in: RoundedRectangle(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(WB.border, lineWidth: 1))
    }
}
