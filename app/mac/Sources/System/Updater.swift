import AppKit
import Foundation
import Security

/// Installs signed, notarized releases from GitHub and restarts the app.
/// Dev builds (version ends in "-dev") only report a newer release.
@Observable
@MainActor
final class Updater {
    enum State: Equatable {
        case idle, checking, upToDate
        case available(String)    // auto-install is off, or this is a dev build
        case downloading(String)
        case ready(String)        // verified and staged, installs when the user is not looking
        case failed(String)
    }

    nonisolated static let repo = "towertail/towertail"
    nonisolated static let assetName = "Towertail.zip"
    nonisolated static let requirement = "anchor apple generic and identifier \"com.towertail.Towertail\" and certificate leaf[subject.OU] = \"Q56WK6TB88\""
    static let checkInterval: TimeInterval = 6 * 3600
    private static let autoInstallKey = "autoInstallUpdates"

    let current: String
    let appURL: URL
    private(set) var state: State = .idle
    private(set) var lastCheck: Date?
    private var staged: URL?
    private var timer: Timer?

    var autoInstall: Bool {
        didSet { UserDefaults.standard.set(autoInstall, forKey: Self.autoInstallKey) }
    }

    init(bundle: Bundle = .main) {
        current = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0"
        appURL = bundle.bundleURL
        autoInstall = UserDefaults.standard.object(forKey: Self.autoInstallKey) as? Bool ?? true
    }

    var isDev: Bool { current.hasSuffix("-dev") }

    var busy: Bool {
        switch state {
        case .checking, .downloading: return true
        default: return false
        }
    }

    var statusText: String {
        switch state {
        case .idle: return isDev ? "Dev build. Updates are not installed." : ""
        case .checking: return "Checking…"
        case .upToDate: return "Up to date"
        case .available(let v): return "v\(v) is available"
        case .downloading(let v): return "Downloading v\(v)…"
        case .ready(let v): return "v\(v) installs when Towertail is not in use"
        case .failed(let e): return "Update failed: \(e)"
        }
    }

    func start() {
        guard timer == nil else { return }
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(15))
            self?.check(manual: false)
        }
        timer = Timer.scheduledTimer(withTimeInterval: 600, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.tick() }
        }
        timer?.tolerance = 60
    }

    private func tick() {
        if case .ready = state { installIfIdle(); return }
        if Date().timeIntervalSince(lastCheck ?? .distantPast) >= Self.checkInterval { check(manual: false) }
    }

    func check(manual: Bool) {
        guard !busy else { return }
        if case .ready = state { return }
        state = .checking
        lastCheck = Date()
        Task { await runCheck(manual: manual) }
    }

    private func runCheck(manual: Bool) async {
        var req = URLRequest(url: URL(string: "https://api.github.com/repos/\(Self.repo)/releases/latest")!)
        req.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        req.setValue("Towertail/\(current)", forHTTPHeaderField: "User-Agent")
        let rel: Release
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
            guard code == 200 else { return fail("GitHub returned \(code)") }
            rel = try JSONDecoder().decode(Release.self, from: data)
        } catch {
            return fail(error.localizedDescription)
        }
        let latest = rel.version
        guard Self.isNewer(latest, than: current) else { state = .upToDate; return }
        guard !isDev, autoInstall || manual,
              let asset = rel.assets.first(where: { $0.name == Self.assetName }) else {
            state = .available(latest)
            return
        }
        await download(asset.browserDownloadURL, version: latest)
    }

    private func download(_ url: URL, version: String) async {
        state = .downloading(version)
        Logger.shared.info("updater: downloading", category: "update", kv: ["version": version])
        let appURL = self.appURL
        do {
            let (download, _) = try await URLSession.shared.download(from: url)
            let tmp = FileManager.default.temporaryDirectory.appendingPathComponent("towertail-\(version).zip")
            try? FileManager.default.removeItem(at: tmp)
            try FileManager.default.moveItem(at: download, to: tmp)
            let app = try await Task.detached { try Self.stage(zip: tmp, appURL: appURL, version: version) }.value
            staged = app
            state = .ready(version)
            Logger.shared.info("updater: verified and staged", category: "update", kv: ["version": version])
            installIfIdle()
        } catch {
            fail("\(error)")
        }
    }

    /// Unpacks the zip next to the app (same volume, so the swap is a rename) and verifies it.
    private nonisolated static func stage(zip: URL, appURL: URL, version: String) throws -> URL {
        let fm = FileManager.default
        defer { try? fm.removeItem(at: zip) }
        let dir = try fm.url(for: .itemReplacementDirectory, in: .userDomainMask, appropriateFor: appURL, create: true)
        let out = try run("/usr/bin/ditto", ["-x", "-k", zip.path, dir.path])
        guard out.ok else { throw UpdateError("unzip failed: \(out.text)") }
        let app = dir.appendingPathComponent("Towertail.app")
        guard fm.fileExists(atPath: app.path) else { throw UpdateError("Towertail.app is missing from the zip") }
        try verify(app, version: version)
        return app
    }

    /// The signature check is the trust boundary: only a bundle signed by our team installs.
    nonisolated static func verify(_ app: URL, version: String) throws {
        var code: SecStaticCode?
        var req: SecRequirement?
        guard SecStaticCodeCreateWithPath(app as CFURL, [], &code) == errSecSuccess, let code,
              SecRequirementCreateWithString(requirement as CFString, [], &req) == errSecSuccess else {
            throw UpdateError("cannot read the code signature")
        }
        let flags = SecCSFlags(rawValue: kSecCSCheckAllArchitectures | kSecCSStrictValidate | kSecCSCheckNestedCode)
        let status = SecStaticCodeCheckValidity(code, flags, req)
        guard status == errSecSuccess else { throw UpdateError("signature is not valid (\(status))") }
        let info = NSDictionary(contentsOf: app.appendingPathComponent("Contents/Info.plist"))
        guard info?["CFBundleShortVersionString"] as? String == version else {
            throw UpdateError("bundle version does not match v\(version)")
        }
        let notarized = try run("/usr/sbin/spctl", ["--assess", "--type", "execute", app.path])
        guard notarized.ok else { throw UpdateError("not notarized: \(notarized.text)") }
    }

    /// The user is not looking when the app is in the background and no window is open.
    private var canRestart: Bool {
        !NSApp.isActive && !NSApp.windows.contains { $0.isVisible && $0.styleMask.contains(.titled) }
    }

    func installIfIdle() {
        guard case .ready = state, canRestart else { return }
        install()
    }

    /// A detached script waits for this process to exit, swaps the bundles, and opens the new app.
    /// If the new app is not running after 15 seconds, the script puts the old app back.
    func install() {
        guard case .ready(let version) = state, let staged else { return }
        let parent = appURL.deletingLastPathComponent().path
        guard !appURL.path.contains("/AppTranslocation/"), FileManager.default.isWritableFile(atPath: parent) else {
            return fail("cannot write to \(parent)")
        }
        let script = """
        pid=$1; app=$2; new=$3; backup="$2.previous"
        while kill -0 "$pid" 2>/dev/null; do sleep 0.2; done
        rm -rf "$backup"
        mv "$app" "$backup" || { open "$app"; exit 1; }
        if ! mv "$new" "$app"; then mv "$backup" "$app"; open "$app"; exit 1; fi
        open "$app"
        sleep 15
        if pgrep -f "$app/Contents/MacOS/Towertail" >/dev/null; then rm -rf "$backup" "$(dirname "$new")"
        else rm -rf "$app"; mv "$backup" "$app"; open "$app"; fi
        """
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/sh")
        p.arguments = ["-c", script, "sh", "\(getpid())", appURL.path, staged.path]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return fail("cannot start the installer: \(error.localizedDescription)") }
        Logger.shared.info("updater: installing and restarting", category: "update", kv: ["version": version])
        NSApp.terminate(nil)
    }

    private func fail(_ msg: String) {
        Logger.shared.error("updater: \(msg)", category: "update")
        state = .failed(msg)
    }

    nonisolated static func isNewer(_ a: String, than b: String) -> Bool {
        let parts = { (s: String) in s.split(separator: "-")[0].split(separator: ".").map { Int($0) ?? 0 } }
        let x = parts(a), y = parts(b)
        for i in 0..<max(x.count, y.count) {
            let l = i < x.count ? x[i] : 0, r = i < y.count ? y[i] : 0
            if l != r { return l > r }
        }
        return false
    }

    private nonisolated static func run(_ path: String, _ args: [String]) throws -> (ok: Bool, text: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: path)
        p.arguments = args
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = pipe
        try p.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (p.terminationStatus == 0, String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines))
    }

    struct Release: Decodable {
        let tagName: String
        let assets: [Asset]
        var version: String { tagName.hasPrefix("v") ? String(tagName.dropFirst()) : tagName }
        enum CodingKeys: String, CodingKey { case tagName = "tag_name", assets }
    }

    struct Asset: Decodable {
        let name: String
        let browserDownloadURL: URL
        enum CodingKeys: String, CodingKey { case name, browserDownloadURL = "browser_download_url" }
    }

    struct UpdateError: Error, CustomStringConvertible {
        let description: String
        init(_ d: String) { description = d }
    }
}
