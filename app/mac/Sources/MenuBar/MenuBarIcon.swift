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
        // Canvas sized so an 18pt glyph fills the menu bar nicely and the
        // bottom-right badge still sits fully inside the bounds. Menu bar
        // draw area is ~22pt tall; we stay at 20 so there's a 1pt gap
        // top/bottom and the badge doesn't get clipped.
        let canvas = NSSize(width: 22, height: 20)
        let glyphSize = NSSize(width: 18, height: 18)
        let image = NSImage(size: canvas)
        image.isTemplate = false
        image.lockFocus()

        let base = NSImage(systemSymbolName: "server.rack", accessibilityDescription: nil)!
        let tint = NSColor(state.tint)
        // Menu bar background is effectively dark on macOS (even in light
        // mode the system vibrancy keeps it near-black), so force white for
        // nominal instead of labelColor which resolves to black when the
        // app's effective appearance is light.
        let glyph = base.tinted(with: state == .nominal ? NSColor.white : tint)
        let glyphRect = NSRect(
            x: 0,
            y: canvas.height - glyphSize.height,
            width: glyphSize.width,
            height: glyphSize.height
        )
        glyph.draw(in: glyphRect, from: .zero, operation: .sourceOver, fraction: 1.0)

        // Permanent status badge: green at nominal, yellow at warn, red at
        // critical. Sits fully inside the canvas with enough margin for the
        // white halo so nothing clips.
        let badgeDiameter: CGFloat = 8
        let badgeRect = NSRect(
            x: canvas.width - badgeDiameter - 2,
            y: 2,
            width: badgeDiameter,
            height: badgeDiameter
        )
        NSColor.windowBackgroundColor.setFill()
        NSBezierPath(ovalIn: badgeRect.insetBy(dx: -1.2, dy: -1.2)).fill()
        tint.setFill()
        NSBezierPath(ovalIn: badgeRect).fill()

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
