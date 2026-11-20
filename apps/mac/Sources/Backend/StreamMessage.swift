import Foundation

/// An event frame of `/v1/stream`. `metric` and `tint` are absent for some
/// kinds. A reachability change has the metric "reachability" and the
/// tint "critical" or "ok".
struct StreamEvent: Decodable, Equatable, Sendable {
    let nodeId: UUID
    let metric: String?
    let tint: String?
    let kind: String
}

/// One text frame of `/v1/stream` (docs/wire.md §4), decoded.
enum StreamMessage {
    case sample(UUID, Sample)
    case nodeUpdated(RemoteNode)
    case settingsUpdated(RemoteServerSettings)
    case diskScan(DiskScanJob)
    case event(StreamEvent)
    case other

    @MainActor private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .iso8601
        return d
    }()

    @MainActor
    static func decode(_ raw: String) -> StreamMessage {
        guard let data = raw.data(using: .utf8) else { return .other }
        struct Envelope: Decodable {
            let type: String
            let nodeId: UUID?
        }
        guard let env = try? decoder.decode(Envelope.self, from: data) else { return .other }
        switch env.type {
        case "sample":
            guard let id = env.nodeId else { return .other }
            struct SampleWrap: Decodable { let sample: Sample }
            // The sample codec parses the timestamps.
            do {
                return .sample(id, try SampleCodec.decoder().decode(SampleWrap.self, from: data).sample)
            } catch {
                Logger.shared.debug("remote: sample decode failed: \(error)", category: "collector")
                return .other
            }
        case "node_updated":
            struct Wrap: Decodable { let node: RemoteNode }
            return (try? decoder.decode(Wrap.self, from: data)).map { .nodeUpdated($0.node) } ?? .other
        case "settings_updated":
            struct Wrap: Decodable { let settings: RemoteServerSettings }
            return (try? decoder.decode(Wrap.self, from: data)).map { .settingsUpdated($0.settings) } ?? .other
        case "disk_scan_updated":
            struct Wrap: Decodable { let job: DiskScanJob }
            return (try? decoder.decode(Wrap.self, from: data)).map { .diskScan($0.job) } ?? .other
        case "event":
            struct Wrap: Decodable { let event: StreamEvent }
            return (try? decoder.decode(Wrap.self, from: data)).map { .event($0.event) } ?? .other
        default:
            return .other
        }
    }
}
