import SwiftUI
import AppKit

struct MenuBarIcon: View {
    // Observing the store directly (rather than taking aggregateState as a
    // plain let) is what makes the label re-render when any server's state
    // changes. MenuBarExtra's label is a long-lived view; without an
    // @Observable dependency here the icon would only pick up the state at
    // app launch and stay stuck on nominal.
    let store: ServerStore
    let settings: ClientSettings
    // Follows the menu bar's own appearance, which depends on the wallpaper
    // and can differ from the app's light/dark mode.
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        Image(nsImage: Self.rendered(
            state: store.aggregateState,
            style: settings.menuBarIconStyle,
            dark: colorScheme == .dark
        ))
    }

    /// Draws the rack-and-pulse mark with template rendering disabled so the
    /// menu bar keeps our colors. SwiftUI's `Image` + `foregroundStyle`
    /// doesn't work here: MenuBarExtra flattens its label to a template
    /// bitmap, stripping both symbol tints and filled-shape colors.
    static func rendered(state: AggregateState, style: MenuBarIconStyle, dark: Bool) -> NSImage {
        let appearance = NSAppearance(named: dark ? .darkAqua : .aqua)!
        let image = NSImage(size: NSSize(width: 18, height: 18), flipped: true) { _ in
            appearance.performAsCurrentDrawingAppearance {
                let tint = NSColor(state.tint)
                let neutral: NSColor = dark ? .white : .black
                let rack = (style == .full && state != .nominal) ? tint : neutral
                drawMark(rack: rack, tint: tint)
            }
            return true
        }
        image.isTemplate = false
        return image
    }

    /// Same geometry as the 20×20 logo on the website, scaled to 18 pt.
    private static func drawMark(rack: NSColor, tint: NSColor) {
        let t = NSAffineTransform()
        t.scale(by: 0.9)
        t.concat()

        rack.setStroke()
        for y in [2.0, 9.0] {
            let unit = NSBezierPath(roundedRect: NSRect(x: 2.5, y: y, width: 15, height: 5), xRadius: 1.8, yRadius: 1.8)
            unit.lineWidth = 1.7
            unit.stroke()
        }

        tint.setFill()
        for y in [4.5, 11.5] {
            NSBezierPath(ovalIn: NSRect(x: 5.6 - 1.2, y: y - 1.2, width: 2.4, height: 2.4)).fill()
        }

        let pulse = NSBezierPath()
        pulse.move(to: NSPoint(x: 2, y: 16.9))
        for (x, y) in [(7, 16.9), (8.4, 15.2), (10.1, 18.8), (11.6, 16.1), (12.5, 16.9), (18, 16.9)] {
            pulse.line(to: NSPoint(x: x, y: y))
        }
        pulse.lineWidth = 1.7
        pulse.lineCapStyle = .round
        pulse.lineJoinStyle = .round
        tint.setStroke()
        pulse.stroke()
    }
}
