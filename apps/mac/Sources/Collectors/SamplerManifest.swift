import Foundation

/// Shape of `dist/samplers/manifest.json` bundled inside the app. Produced by
/// `scripts/build.sampler.sh` and kept in lockstep with the per-triple
/// binaries under `samplers/<triple>/towertail-sampler`.
struct SamplerManifest: Decodable, Sendable, Equatable {
    let version: String
    let sha: String
    let binaries: [String: String]

    /// The version+SHA string the running sampler reports in `host.sampler` —
    /// sampler-side prints `version.Version + "+" + version.SHA`, so we must
    /// match that format when comparing.
    var expectedSamplerField: String {
        "\(version)+\(sha)"
    }
}

enum SamplerManifestLoader {
    /// Locates `samplers/manifest.json` next to the bundled per-triple binaries.
    /// Falls back to a dev-time path under `dist/` so local Xcode runs work
    /// even before the prebuild rsync has populated the Resources folder.
    static func load() -> SamplerManifest? {
        if let url = Bundle.main.url(
            forResource: "manifest",
            withExtension: "json",
            subdirectory: "samplers"
        ), let m = decode(url) {
            return m
        }
        if let resourceURL = Bundle.main.resourceURL {
            let candidate = resourceURL
                .appendingPathComponent("samplers", isDirectory: true)
                .appendingPathComponent("manifest.json")
            if let m = decode(candidate) { return m }
        }
        let fm = FileManager.default
        var url = URL(fileURLWithPath: fm.currentDirectoryPath)
        for _ in 0..<8 {
            let candidate = url.appendingPathComponent("dist/samplers/manifest.json")
            if let m = decode(candidate) { return m }
            let parent = url.deletingLastPathComponent()
            if parent.path == url.path { break }
            url = parent
        }
        return nil
    }

    private static func decode(_ url: URL) -> SamplerManifest? {
        guard let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(SamplerManifest.self, from: data)
    }
}
