import SwiftUI

/// Squarified treemap layout (Bruls, Huizing, van Wijk). Pure, so tests
/// can check it.
enum TreemapLayout {
    struct Item: Equatable, Sendable {
        var id: Int
        var value: Double
    }

    struct Placed: Equatable, Sendable {
        var id: Int
        var rect: CGRect
    }

    /// Places the items in `rect`, largest first. The area of each tile
    /// is proportional to its value. Items with a value of 0 or less get
    /// no tile.
    static func squarify(_ items: [Item], in rect: CGRect) -> [Placed] {
        let usable = items.filter { $0.value > 0 && $0.value.isFinite }
        let total = usable.reduce(0) { $0 + $1.value }
        guard total > 0, rect.width > 0, rect.height > 0 else { return [] }
        let scale = Double(rect.width * rect.height) / total
        let scaled = usable
            .map { (id: $0.id, a: $0.value * scale) }
            .sorted { $0.a > $1.a }

        var out: [Placed] = []
        out.reserveCapacity(scaled.count)
        var free = rect
        var row: [(id: Int, a: Double)] = []

        func worst(_ r: [(id: Int, a: Double)], _ side: Double) -> Double {
            let s = r.reduce(0) { $0 + $1.a }
            var m = 0.0
            for e in r {
                m = max(m, side * side * e.a / (s * s), (s * s) / (side * side * e.a))
            }
            return m
        }

        func flush() {
            let s = row.reduce(0) { $0 + $1.a }
            if free.width >= free.height {
                let w = CGFloat(s) / free.height
                var y = free.minY
                for e in row {
                    let h = CGFloat(e.a) / w
                    out.append(Placed(id: e.id, rect: CGRect(x: free.minX, y: y, width: w, height: h)))
                    y += h
                }
                free = CGRect(x: free.minX + w, y: free.minY, width: max(0, free.width - w), height: free.height)
            } else {
                let h = CGFloat(s) / free.width
                var x = free.minX
                for e in row {
                    let w = CGFloat(e.a) / h
                    out.append(Placed(id: e.id, rect: CGRect(x: x, y: free.minY, width: w, height: h)))
                    x += w
                }
                free = CGRect(x: free.minX, y: free.minY + h, width: free.width, height: max(0, free.height - h))
            }
            row = []
        }

        for item in scaled {
            let side = Double(min(free.width, free.height))
            if row.isEmpty || worst(row + [item], side) <= worst(row, side) {
                row.append(item)
            } else {
                flush()
                row.append(item)
            }
        }
        if !row.isEmpty { flush() }
        return out
    }
}

extension DiskCategory {
    var rgb: (Double, Double, Double) {
        switch self {
        case .code: return (124, 111, 230)
        case .ai: return (214, 112, 166)
        case .cache: return (91, 143, 209)
        case .sdk: return (79, 179, 184)
        case .docs: return (208, 138, 75)
        case .other: return (140, 140, 148)
        case .none: return (120, 120, 126)
        }
    }

    func color(_ alpha: Double) -> Color {
        let (r, g, b) = rgb
        return Color(.sRGB, red: r / 255, green: g / 255, blue: b / 255, opacity: alpha)
    }
}

/// Two-level treemap of one folder: its children, and inside each large
/// child the grandchildren. Click a tile that has children to open it.
struct TreemapView: View {
    let nodes: [DiskNode]
    /// Node ids that the selected recommendations point to.
    let selectedIDs: Set<String>
    var onOpen: (Int) -> Void = { _ in }

    static let selectedGreen = Color(.sRGB, red: 52 / 255, green: 199 / 255, blue: 89 / 255, opacity: 1)

    struct Tile {
        var index: Int
        var rect: CGRect
        var node: DiskNode
        var isParent: Bool
        var selected: Bool
        var kids: [(rect: CGRect, node: DiskNode, selected: Bool)]
    }

    static func tiles(nodes: [DiskNode], selectedIDs: Set<String>, size: CGSize) -> [Tile] {
        let items = nodes.enumerated().map { TreemapLayout.Item(id: $0.offset, value: Double($0.element.bytes)) }
        return TreemapLayout.squarify(items, in: CGRect(origin: .zero, size: size)).map { p in
            let node = nodes[p.id]
            let r = p.rect.insetBy(dx: 2, dy: 2)
            let isParent = !node.children.isEmpty && r.width > 110 && r.height > 80
            let own = node.id.map(selectedIDs.contains) ?? false
            let sel = own || (!isParent && !node.subtreeIDs.isDisjoint(with: selectedIDs))
            var kids: [(CGRect, DiskNode, Bool)] = []
            if isParent {
                let inner = CGRect(x: r.minX + 6, y: r.minY + 26, width: r.width - 12, height: r.height - 32)
                let kitems = node.children.enumerated().map { TreemapLayout.Item(id: $0.offset, value: Double($0.element.bytes)) }
                for k in TreemapLayout.squarify(kitems, in: inner) {
                    let kn = node.children[k.id]
                    let ksel = sel || !kn.subtreeIDs.isDisjoint(with: selectedIDs)
                    kids.append((k.rect.insetBy(dx: 1.5, dy: 1.5), kn, ksel))
                }
            }
            return Tile(index: p.id, rect: r, node: node, isParent: isParent, selected: sel, kids: kids)
        }
    }

    var body: some View {
        GeometryReader { geo in
            let tiles = Self.tiles(nodes: nodes, selectedIDs: selectedIDs, size: geo.size)
            Canvas { ctx, _ in
                for t in tiles { draw(t, in: &ctx) }
            }
            .contentShape(Rectangle())
            .onTapGesture(coordinateSpace: .local) { pt in
                if let t = tiles.first(where: { $0.rect.contains(pt) }), !t.node.children.isEmpty {
                    onOpen(t.index)
                }
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(nodes.map { "\($0.name), \(DiskSize.text($0.bytes))" }.joined(separator: "; "))
        }
    }

    private func draw(_ t: Tile, in ctx: inout GraphicsContext) {
        let alpha = t.isParent ? 0.14 : 0.3
        fill(t.rect, radius: 7, cat: t.node.cat, alpha: alpha, selected: t.selected, in: &ctx)
        let r = t.rect
        let big = !t.isParent && r.height > 64 && r.width > 80
        if r.width > 46 && r.height > 22 {
            let small = t.isParent || (!big && r.width > 120)
            var nameRect = CGRect(x: r.minX + 8, y: r.minY + 5, width: r.width - 16, height: 16)
            if small {
                let size = ctx.resolve(Text(DiskSize.text(t.node.bytes))
                    .font(.system(size: 11, design: .monospaced)).foregroundColor(Color(white: 0.78)))
                let w = size.measure(in: CGSize(width: 200, height: 20)).width
                ctx.draw(size, at: CGPoint(x: r.maxX - 8, y: r.minY + 13), anchor: .trailing)
                nameRect.size.width = max(0, nameRect.width - w - 6)
            }
            ctx.draw(Text(t.node.name).font(.system(size: 12, weight: .semibold)).foregroundColor(Color(white: 0.95)),
                     in: nameRect)
        }
        if big {
            let fs: CGFloat = r.width * r.height > 40_000 ? 24 : 16
            ctx.draw(Text(DiskSize.text(t.node.bytes)).font(.system(size: fs, weight: .bold, design: .monospaced))
                        .foregroundColor(Color(white: 0.95)),
                     at: CGPoint(x: r.minX + 8, y: r.maxY - 6), anchor: .bottomLeading)
        }
        for k in t.kids {
            fill(k.rect, radius: 5, cat: k.node.cat, alpha: 0.3, selected: k.selected, in: &ctx)
            let kr = k.rect
            if kr.width > 44 && kr.height > 20 {
                ctx.draw(Text(k.node.name).font(.system(size: 11)).foregroundColor(Color(white: 0.95)),
                         in: CGRect(x: kr.minX + 6, y: kr.minY + 4, width: kr.width - 12, height: 14))
            }
            if kr.height > 50 && kr.width > 70 {
                let fs: CGFloat = kr.width * kr.height > 22_000 ? 18 : 13
                ctx.draw(Text(DiskSize.text(k.node.bytes)).font(.system(size: fs, weight: .bold, design: .monospaced))
                            .foregroundColor(Color(white: 0.95)),
                         at: CGPoint(x: kr.minX + 6, y: kr.maxY - 4), anchor: .bottomLeading)
            }
        }
    }

    private func fill(_ r: CGRect, radius: CGFloat, cat: DiskCategory, alpha: Double, selected: Bool,
                      in ctx: inout GraphicsContext) {
        guard r.width > 0, r.height > 0 else { return }
        let shape = Path(roundedRect: r, cornerRadius: radius)
        if selected {
            Self.stripes(r, shape: shape, angle: 135, a: Self.selectedGreen.opacity(0.42),
                         b: Self.selectedGreen.opacity(0.14), band: 6, in: &ctx)
            ctx.stroke(shape, with: .color(Self.selectedGreen), lineWidth: 1)
        } else if cat == .none {
            Self.stripes(r, shape: shape, angle: 45, a: cat.color(0.28), b: cat.color(0.08), band: 5, in: &ctx)
            ctx.stroke(shape, with: .color(cat.color(0.6)), lineWidth: 1)
        } else {
            ctx.fill(shape, with: .color(cat.color(alpha)))
            ctx.stroke(shape, with: .color(cat.color(0.6)), lineWidth: 1)
        }
    }

    /// Diagonal stripes like a CSS repeating-linear-gradient.
    static func stripes(_ r: CGRect, shape: Path, angle: Double, a: Color, b: Color, band: CGFloat,
                        in ctx: inout GraphicsContext) {
        ctx.drawLayer { layer in
            layer.clip(to: shape)
            layer.fill(shape, with: .color(b))
            var p = Path()
            let span = r.width + r.height
            var x = -r.height
            let rising = angle == 45
            while x < span {
                if rising {
                    p.move(to: CGPoint(x: r.minX + x, y: r.maxY))
                    p.addLine(to: CGPoint(x: r.minX + x + r.height, y: r.minY))
                } else {
                    p.move(to: CGPoint(x: r.minX + x, y: r.minY))
                    p.addLine(to: CGPoint(x: r.minX + x + r.height, y: r.maxY))
                }
                x += band * 2 * 1.414
            }
            layer.stroke(p, with: .color(a), lineWidth: band)
        }
    }
}
