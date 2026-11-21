import Foundation

/// The hosts picked for Cloud, capped by the room of the plan.
struct CloudPicks: Equatable {
    /// The hosts that the plan can still take. Nil means no cap.
    private(set) var room: Int?
    private(set) var ids: Set<UUID>

    /// The default picks are the first hosts that fit in the room.
    init(hosts: [Node] = [], room: Int?) {
        self.room = room
        self.ids = Set(hosts.prefix(room ?? hosts.count).map(\.id))
    }

    var isFull: Bool { ids.count >= (room ?? .max) }

    mutating func toggle(_ id: UUID) {
        if ids.contains(id) {
            ids.remove(id)
        } else if !isFull {
            ids.insert(id)
        }
    }

    mutating func clear() { ids = [] }

    var counter: String {
        let n = ids.count
        guard let room else { return "\(n) Cloud \(n == 1 ? "host" : "hosts") selected" }
        return "\(n) of \(room) Cloud \(room == 1 ? "host" : "hosts") selected"
    }

    /// The text in the info box above the hosts.
    static func note(room: Int?, planLabel: String, used: Bool) -> String {
        let intro = "Cloud hosts get 24/7 alerts, long history and the web and phone apps."
        guard let room else { return "\(intro) SSH hosts have no limit." }
        let name = planLabel == "Plan" ? "Your plan" : "The \(planLabel)"
        if room == 0 { return "\(name) is full. Turn off 24/7 monitoring on another host first." }
        let hosts = room == 1 ? "Cloud host" : "Cloud hosts"
        let plan = used ? "\(name) has room for \(room) more \(hosts)." : "\(name) takes \(room) \(hosts)."
        return "\(intro) \(plan) SSH hosts have no limit."
    }
}
