import Foundation

/// Refresh of one folder: the request for the folder scan, and the merge
/// of its result into the stored scan.
extension DiskScanResult {
    var separator: String { isWindows ? "\\" : "/" }

    /// Absolute path of the node at `names` (names from the root).
    func absolutePath(of names: [String]) -> String {
        guard !names.isEmpty else { return root }
        let rest = names.joined(separator: separator)
        return root.hasSuffix(separator) ? root + rest : root + separator + rest
    }

    /// The path as the recs show it: `~` (or `%USERPROFILE%`) below home.
    func displayPath(_ path: String) -> String {
        guard !home.isEmpty else { return path }
        let tilde = isWindows ? "%USERPROFILE%" : "~"
        if path == home { return tilde }
        if path.hasPrefix(home + separator) { return tilde + path.dropFirst(home.count) }
        return path
    }

    /// Names from the root to the node at the absolute `path`, or nil when
    /// the tree has no such node. A name can be a chain such as `a/b`.
    func names(of path: String) -> [String]? {
        if path == root { return [] }
        let base = root.hasSuffix(separator) ? root : root + separator
        guard path.hasPrefix(base) else { return nil }
        var segs = path.dropFirst(base.count).split(separator: Character(separator)).map(String.init)
        var node = tree
        var out: [String] = []
        while !segs.isEmpty {
            let hit = node.children.first { k in
                guard !k.isOther else { return false }
                let ks = k.name.split(separator: Character(separator)).map(String.init)
                return ks.count <= segs.count && Array(segs.prefix(ks.count)) == ks
            }
            guard let hit else { return nil }
            out.append(hit.name)
            segs.removeFirst(hit.name.split(separator: Character(separator)).count)
            node = hit
        }
        return out
    }

    /// The scan request for the folder at `names`. It uses the limits of
    /// this scan, so that the merged tree has the same detail.
    func refreshRequest(for names: [String]) -> DiskScanRequest {
        let depth = names.reduce(0) { $0 + $1.split(separator: Character(separator)).count }
        let maxDepth = limits?.maxDepth ?? 6
        let minBytes = limits?.minBytes ?? max(1, min(50_000_000, Int64(0.002 * Double(tree.bytes))))
        return DiskScanRequest(root: absolutePath(of: names), maxDepth: max(1, maxDepth - depth),
                               minBytes: minBytes, minFrac: 0)
    }

    /// Puts `sub`, the scan of the folder at `names`, in place of that
    /// folder. The size change goes to each parent. Returns nil when the
    /// tree has no such folder. Files and dirs stay the same, because the
    /// counts of the old folder are not known.
    func merging(_ sub: DiskScanResult, at names: [String], now: Date) -> DiskScanResult? {
        guard let old = tree.node(at: names[...]) else { return nil }
        // A count of `refreshed` can repeat, because a refresh drops the entries below it.
        let used = tree.subtreeIDs.compactMap { id -> Int? in
            guard id.hasPrefix("r"), let dash = id.firstIndex(of: "-") else { return nil }
            return Int(id[id.index(after: id.startIndex)..<dash])
        }
        let tag = "r\((used.max() ?? 0) + 1)-"
        let delta = sub.tree.bytes - old.bytes
        let kids = sub.tree.c.map { $0.map { $0.retagged(tag, otherAs: old.cat) } }
        let target = displayPath(absolutePath(of: names))
        let below = old.subtreeIDs.subtracting(old.id.map { [$0] } ?? [])
        let above = tree.ids(along: names[...]).union(old.id.map { [$0] } ?? [])
        let sizes = old.sizesByID

        var out = self
        out.tree.replace(at: names[...], kids: kids, delta: delta)
        out.scannedBytes = max(0, scannedBytes + delta)

        var recs: [DiskRec] = []
        for var r in self.recs {
            if r.nodeIDs.isEmpty {
                if Self.path(r.path, isIn: target, separator: separator) { continue }
                recs.append(r)
                continue
            }
            let gone = Set(r.nodeIDs).intersection(below)
            if gone.count == r.nodeIDs.count { continue }
            if !gone.isEmpty {
                r.nodeIDs.removeAll { gone.contains($0) }
                r.bytes = max(0, r.bytes - gone.reduce(0) { $0 + (sizes[$1] ?? 0) })
            }
            if !above.isDisjoint(with: r.nodeIDs) { r.bytes = max(0, r.bytes + delta) }
            recs.append(r)
        }
        var ids = Set(recs.map(\.id))
        for var r in sub.recs {
            if recs.contains(where: { $0.ruleID == r.ruleID && $0.path == r.path }) { continue }
            r.nodeIDs = r.nodeIDs.map { tag + $0 }
            var id = r.id
            var n = 2
            while ids.contains(id) {
                id = "\(r.id)-\(n)"
                n += 1
            }
            r.id = id
            ids.insert(id)
            recs.append(r)
        }
        out.recs = recs

        out.errors += sub.errors.filter { !errors.contains($0) }
        let abs = absolutePath(of: names)
        out.refreshed.removeAll { Self.path($0.path, isIn: abs, separator: separator) }
        out.refreshed.append(DiskScanRefresh(path: abs, at: now))
        return out
    }

    /// True when `path` is `folder` or below it. A trailing `*` is ignored.
    static func path(_ path: String, isIn folder: String, separator: String) -> Bool {
        var p = path
        if p.hasSuffix(separator + "*") { p.removeLast(2) }
        return p == folder || p.hasPrefix(folder.hasSuffix(separator) ? folder : folder + separator)
    }
}

extension DiskRec {
    /// The rule id without the number that the sampler adds to repeats.
    var ruleID: String {
        guard let r = id.range(of: #"-\d+$"#, options: .regularExpression) else { return id }
        return String(id[..<r.lowerBound])
    }
}

extension DiskNode {
    func node(at names: ArraySlice<String>) -> DiskNode? {
        guard let first = names.first else { return self }
        return children.first { $0.name == first && !$0.isOther }?.node(at: names.dropFirst())
    }

    /// Ids of the nodes from here to the node at `names`, without that node.
    func ids(along names: ArraySlice<String>) -> Set<String> {
        guard let first = names.first,
              let k = children.first(where: { $0.name == first && !$0.isOther }) else { return [] }
        var out = k.ids(along: names.dropFirst())
        if let id { out.insert(id) }
        return out
    }

    var sizesByID: [String: Int64] {
        var out: [String: Int64] = [:]
        if let id { out[id] = b }
        for k in children { out.merge(k.sizesByID) { a, _ in a } }
        return out
    }

    /// Sets the children of the node at `names` and adds `delta` to it and
    /// to each node above it. The changed siblings are sorted again.
    mutating func replace(at names: ArraySlice<String>, kids: [DiskNode]?, delta: Int64) {
        b = max(0, b + delta)
        guard let first = names.first else {
            c = kids
            return
        }
        guard var list = c, let i = list.firstIndex(where: { $0.name == first && !$0.isOther }) else { return }
        list[i].replace(at: names.dropFirst(), kids: kids, delta: delta)
        list.sort { ($0.isOther ? 1 : 0, -$0.b) < ($1.isOther ? 1 : 0, -$1.b) }
        c = list
    }

    /// A copy with `prefix` on each id. Below a folder with a known
    /// category, `other` takes that category, as in a full scan.
    func retagged(_ prefix: String, otherAs cat: DiskCategory) -> DiskNode {
        var n = self
        n.id = id.map { prefix + $0 }
        if n.cat == .other, cat != .none { n.cat = cat }
        n.c = c?.map { $0.retagged(prefix, otherAs: cat) }
        return n
    }
}
