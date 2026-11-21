import Foundation

/// The sampler flag that keeps process folders on the host.
enum ProcFolderFlag {
    static let name = "--no-proc-cwd"

    /// Extra arguments of `--once` for the node.
    static func onceArguments(for node: Node) -> [String] {
        node.sendProcFolders ? [] : [name]
    }

    /// True when the sampler is too old to know the flag.
    static func isUnknownFlag(_ stderr: String) -> Bool {
        stderr.contains("flag provided but not defined") && stderr.contains("no-proc-cwd")
    }
}
