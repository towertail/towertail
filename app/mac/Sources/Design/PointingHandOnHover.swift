import SwiftUI
import AppKit

/// Swaps the cursor to a pointing-hand while the pointer is over this
/// view, matching the affordance users get in a web browser. Reverts to
/// the arrow on exit. Used on clickable surfaces (metric cells, filter
/// pills) where the click target isn't a native Button shape.
extension View {
    func pointingHandOnHover() -> some View {
        modifier(PointingHandOnHoverModifier())
    }
}

private struct PointingHandOnHoverModifier: ViewModifier {
    func body(content: Content) -> some View {
        content.onHover { hovering in
            if hovering {
                NSCursor.pointingHand.set()
            } else {
                NSCursor.arrow.set()
            }
        }
    }
}
