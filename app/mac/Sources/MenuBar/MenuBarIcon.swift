import SwiftUI
import AppKit

struct MenuBarIcon: View {
    // Observing the store directly (rather than taking aggregateState as a
    // plain let) is what makes the label re-render when any server's state
    // changes. MenuBarExtra's label is a long-lived view; without an
    // @Observable dependency here the icon would only pick up the state at
    // app launch and stay stuck on nominal.
    let store: ServerStore

    var body: some View {
        let state = store.aggregateState
        Image(nsImage: Self.rendered(state: state))
    }

    /// Renders the icon into an NSImage with template rendering disabled so
    /// the menu bar keeps our custom colors instead of re-tinting everything
    /// to match the bar appearance. SwiftUI's `Image` + `foregroundStyle`
    /// doesn't work here: MenuBarExtra flattens its label to a template
    /// bitmap, stripping both symbol tints and filled-shape colors.
    private static func rendered(state: AggregateState) -> NSImage {
        // Canvas is larger than the glyph so the bottom-right badge has
        // room to sit outside the server-rack bounds without clipping.
        let canvas = NSSize(width: 20, height: 18)
        let glyphSize = NSSize(width: 16, height: 16)
        let image = NSImage(size: canvas)
        image.isTemplate = false
        image.lockFocus()

        let base = NSImage(systemSymbolName: "server.rack", accessibilityDescription: nil)!
        let tint = NSColor(state.tint)
        // Tint the base glyph: warn → yellow, critical → red, nominal →
        // labelColor so it reads correctly on both light and dark menu bars.
        let glyph = base.tinted(with: state == .nominal ? NSColor.labelColor : tint)
        let glyphRect = NSRect(
            x: 0,
            y: canvas.height - glyphSize.height,
            width: glyphSize.width,
            height: glyphSize.height
        )
        glyph.draw(in: glyphRect, from: .zero, operation: .sourceOver, fraction: 1.0)

        // Tiny solid badge for warn/critical — nudged inside the canvas so
        // it's fully visible after the menu bar draws its label.
        if state != .nominal {
            let badgeDiameter: CGFloat = 8
            let badgeRect = NSRect(
                x: canvas.width - badgeDiameter - 1,
                y: 1,
                width: badgeDiameter,
                height: badgeDiameter
            )
            NSColor.windowBackgroundColor.setFill()
            NSBezierPath(ovalIn: badgeRect.insetBy(dx: -1.2, dy: -1.2)).fill()
            tint.setFill()
            NSBezierPath(ovalIn: badgeRect).fill()
        }

        image.unlockFocus()
        return image
    }
}

private extension NSImage {
    func tinted(with color: NSColor) -> NSImage {
        let tinted = NSImage(size: size)
        tinted.lockFocus()
        color.set()
        let rect = NSRect(origin: .zero, size: size)
        draw(in: rect, from: .zero, operation: .sourceOver, fraction: 1.0)
        rect.fill(using: .sourceAtop)
        tinted.unlockFocus()
        tinted.isTemplate = false
        return tinted
    }
}
