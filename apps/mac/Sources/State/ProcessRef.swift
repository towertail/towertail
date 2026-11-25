import Foundation

/// A process across time. PIDs change, so the key joins name and command.
struct ProcessRef: Hashable, Sendable, Codable {
    let key: String
    let pid: Int32?
    let name: String
    let cmd: String

    static func local(_ p: ProcSample) -> ProcessRef {
        ProcessRef(key: key(of: p), pid: p.pid, name: p.name, cmd: p.cmd ?? "")
    }

    /// The key of Cloud when the row has one, else name plus command.
    static func key(of p: ProcSample) -> String {
        p.key ?? key(name: p.name, cmd: p.cmd)
    }

    static func key(name: String, cmd: String?) -> String {
        name + "\u{0}" + (cmd ?? "")
    }
}
