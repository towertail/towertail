# Backend split: Local vs Remote

Plan for restructuring the Mac app so the same Swift client can run in two modes:

- **Local mode** — `LocalBackend` runs the collector stack in-process (today's behavior). No network server, no account, no configuration beyond picking nodes.
- **Remote mode** — `RemoteBackend` is a thin HTTP/WebSocket client to an external Towertail server. The server owns node config, polling, history, thresholds, and notifications. The Swift app is a console.

This plan covers **only the Swift-side restructuring**. Building the actual remote server is out of scope here (it becomes its own project once the client is ready for it). The goal is to land the `Backend` seam now, against today's embedded implementation, so remote mode becomes a greenfield addition rather than a rewrite.

See also: [`PLAN.md`](PLAN.md) — Phase 3 roadmap. [`sampler.md`](sampler.md) — wire contract with the sampler (unchanged by this plan).

---

## Current state — what's already client-shaped

The UI and state layers are already pure consumers of observable state. They never call SSH, run the sampler, or touch the file system directly for node data:

- `App/AppEnvironment.swift:33` — the one place that wires `RealCollector` to `ServerStore`.
- `MenuBar/`, `Cards/`, `FullView/` — read `ServerStore.serverVMs` and `ServerViewModel` fields. No collector calls.
- `State/ServerStore.swift` — pure accumulator. `ingest(_:for:)` at line 75 is the single ingestion point.
- `State/ServerViewModel.swift` — observable per-node metrics + history.
- `State/HistoryStore.swift` — SQLite persistence, called only from `ServerStore` and `ServerViewModel`.

The collector layer is the only place that drives nodes today:

- `Collectors/Collector.swift` — protocol: `func run(sink: ServerStore) async`.
- `Collectors/RealCollector.swift` — supervisor loop, per-node pacers, calls `sink.ingest(sample, for: nodeID)` at line 123.
- `Collectors/SamplerInvoker.swift` — protocol abstracting SSH vs local sampler execution.
- `Collectors/SamplerUpdateCoordinator.swift` — pushes updated sampler binaries to remote hosts via SSH.

## What's leaky — places the UI/state reach into local-only concerns

These need to be fixed before remote mode is viable. They can all be addressed incrementally, against the local backend, before any server exists.

| # | File | Leak | Why it breaks remote mode |
|---|------|------|---------------------------|
| L1 | `FullView/ProcessTable.swift:485-528` | `killLocal` shells `/bin/kill`; `killSSH` shells `/usr/bin/ssh kill -9`. UI owns the transport. | Remote mode: kill must be a `POST /nodes/{id}/kill` to the server, which does the SSH itself. |
| L2 | `System/ThresholdNotifier.swift` | App fires local `UNUserNotification` based on in-process thresholds via `ServerStore.notifier`. | Remote mode: the server evaluates thresholds and pushes notification events to the client. |
| L3 | `Preferences/ServersPane.swift` + `State/NodeStore.swift` | UI edits `Node` fields (sshUser, sshHost) and persists to a local file. | Remote mode: server is source of truth for node list and SSH credentials. Client fetches/edits via API. |
| L4 | `Collectors/SamplerUpdateCoordinator.swift` | Silent SSH binary push when sampler version mismatches. | Remote mode: the server pushes binaries. Client merely displays version status. |
| L5 | `State/AppSettings.swift` | Mixes client-only prefs (terminal app, card density, favorites) with server-owned settings (polling intervals, thresholds, per-node overrides). | Remote mode: the two groups live in different places (client vs server). |

Not leaky, stays local in both modes: `System/TerminalLauncher.swift` (opening `ssh://` URLs on the Mac), favorites, window positions, menu-bar icon preferences.

---

## Target architecture

### The `Backend` protocol

One actor-isolated protocol that owns **data-in** (observable state the UI reads) and **actions-out** (mutations the UI triggers). Two implementations. The UI never knows which it's talking to.

```swift
@MainActor
protocol Backend: AnyObject {
    // Observable state surfaces (already @Observable today)
    var servers: ServerStore { get }          // live metrics, connection state
    var nodes: NodeStore { get }              // node configuration
    var settings: ServerSettings { get }      // polling intervals, thresholds — server-owned in remote mode

    // Lifecycle
    func start() async
    func stop() async

    // Actions — node management
    func addNode(_ node: Node) async throws
    func updateNode(_ node: Node) async throws
    func deleteNode(id: UUID) async throws

    // Actions — runtime
    func killProcess(nodeID: UUID, pid: Int32) async throws
    func updateThresholds(_ thresholds: Thresholds) async throws
    func updateNodeThresholds(nodeID: UUID, _ thresholds: NodeThresholds?) async throws

    // Events — server-originated notifications, version changes, etc.
    func events() -> AsyncStream<BackendEvent>
}

enum BackendEvent: Sendable {
    case thresholdCrossed(nodeID: UUID, metric: Metric, state: ThresholdState)
    case samplerVersionChanged(nodeID: UUID, version: String)
    case nodeReachabilityChanged(nodeID: UUID, reachable: Bool)
}
```

### Two implementations

**`LocalBackend`** — wraps today's stack:

- Owns `RealCollector`, `ServerStore`, `NodeStore`, `HistoryStore`, `ThresholdNotifier`, `SamplerUpdateCoordinator`.
- `start()` calls `collector.run(sink: store)` (today's `AppEnvironment.start`).
- `killProcess()` does what `ProcessTable.killLocal/killSSH` do today.
- `addNode/updateNode/deleteNode` mutate `NodeStore` and persist to disk.
- `events()` is a bridge from `ThresholdNotifier` and reachability signals.

**`RemoteBackend`** — thin client, built later:

- `servers`/`nodes`/`settings` are populated from REST + WebSocket.
- `start()` opens a WebSocket for live samples and events.
- `killProcess()` is `POST /nodes/{id}/kill-process?pid=…`.
- `addNode/updateNode/deleteNode` hit REST endpoints.
- `events()` is a passthrough of the WebSocket event stream.

### `AppEnvironment` after the split

```swift
@MainActor
final class AppEnvironment {
    let backend: any Backend

    init(mode: BackendMode = .local) {
        switch mode {
        case .local:
            self.backend = LocalBackend()
        case .remote(let url, let token):
            self.backend = RemoteBackend(url: url, token: token)
        }
    }

    func start() { Task { await backend.start() } }
    func stop()  { Task { await backend.stop()  } }
}
```

UI sites that today read `env.store` / `env.nodeStore` / `env.settings` become `env.backend.servers` / `env.backend.nodes` / `env.backend.settings`. Same types, same observability.

### Settings split

Split `AppSettings` into two structs:

- **`ClientSettings`** — always local, persisted on the Mac. Terminal app choice, card density, favorites, menu-bar icon prefs, window positions, launch-at-login.
- **`ServerSettings`** — server-owned in remote mode, persisted locally in local mode. Polling intervals, global thresholds, per-node threshold overrides, notification policies, auto-update-sampler toggle.

This split is a prerequisite for step 4 of the migration below. Until then, continue using `AppSettings` as-is.

---

## Migration plan

Six ordered steps. Each compiles, ships, and is reviewable on its own. Each keeps the local backend working — the only observable change per step is internal structure.

### Step 1 — Introduce `Backend` protocol + `LocalBackend`

**Scope.** Add `Sources/Backend/Backend.swift` with the protocol and `BackendMode` enum. Add `Sources/Backend/LocalBackend.swift` that owns today's `RealCollector`, `ServerStore`, `NodeStore`, `HistoryStore`, `ThresholdNotifier`, `SamplerUpdateCoordinator`, `SystemReachabilityMonitor`. Mutating methods (`addNode`, `updateNode`, `killProcess`, etc.) are stubs that call into the existing concrete types. `events()` returns a real `AsyncStream` wired to the notifier + reachability.

**`AppEnvironment`** shrinks to `let backend: any Backend` plus `start()`/`stop()`. The individual `store`, `nodeStore`, `settings`, `history`, `collector`, `notifier`, `samplerUpdater`, `reachability` properties are **moved inside** `LocalBackend` but temporarily re-exposed via `env.backend` accessors so UI sites don't all change at once.

**Files touched.** `Sources/App/AppEnvironment.swift`, new `Sources/Backend/Backend.swift`, new `Sources/Backend/LocalBackend.swift`.

**Risk.** Low. Pure repackaging; no behavior change. Tests pass unchanged.

**Definition of done.** App builds and runs identically. `LocalBackend` compiles. `Backend` protocol exists with all methods defined.

### Step 2 — Route process kill through `Backend.killProcess()`

**Scope.** Fix leak **L1**. Move `killLocal` and `killSSH` out of `FullView/ProcessTable.swift` and into `LocalBackend.killProcess(nodeID:pid:)`. `ProcessTable` calls `env.backend.killProcess(…)` with no knowledge of local vs SSH.

**Files touched.** `Sources/FullView/ProcessTable.swift`, `Sources/Backend/LocalBackend.swift`.

**Risk.** Low. Single caller, straightforward move. Existing Outcome/error handling preserved.

**Definition of done.** No `/bin/kill` or `/usr/bin/ssh` strings outside the Backend layer. Killing still works in manual test.

### Step 3 — Route node mutations through the backend

**Scope.** Fix leak **L3**. `Preferences/ServersPane.swift`, `ServerEditSheet`, bulk-import paths, CSV import — all switch from mutating `NodeStore` directly to calling `env.backend.addNode` / `updateNode` / `deleteNode`. The `NodeStore` type stays; `LocalBackend` delegates to it. This is a preparatory refactor: today's implementation is synchronous, but the protocol is `async throws` to allow remote mode later.

**Files touched.** `Sources/Preferences/ServersPane.swift`, `Sources/Preferences/ServerEditSheet.swift`, any other callers that mutate `NodeStore.nodes`. `Sources/Backend/LocalBackend.swift`.

**Risk.** Medium. Several call sites, some in sheets. Concurrency change (sync → async) ripples through forms; handle errors with simple `do { try await … } catch { log }` for now.

**Definition of done.** `NodeStore` has no external mutation call sites outside `LocalBackend`. Preferences still work end-to-end.

### Step 4 — Split `AppSettings` into `ClientSettings` + `ServerSettings`

**Scope.** Fix leak **L5**. Create `Sources/State/ClientSettings.swift` and `Sources/State/ServerSettings.swift`. Move fields per the table in the target architecture section. `AppSettings` remains as a facade **temporarily**, or is removed entirely if callsite count is manageable. `LocalBackend` owns both; `RemoteBackend` will own only `ClientSettings` locally and pull `ServerSettings` from the API.

Introduce `Backend.updateThresholds` and `Backend.updateNodeThresholds`. Preferences sliders call these instead of mutating settings directly.

**Files touched.** `Sources/State/AppSettings.swift`, `Sources/State/SettingsPersistence.swift`, `Sources/Preferences/ThresholdsPane.swift`, `Sources/Preferences/NotificationsPane.swift`, threshold-sensitive UI in Cards/FullView. Likely the largest step.

**Risk.** Medium-high. Many read sites. Do this in two commits: (1) introduce the new types alongside `AppSettings`, migrate readers; (2) remove `AppSettings`.

**Definition of done.** `ClientSettings` and `ServerSettings` are distinct. All threshold changes flow through `Backend.updateThresholds`.

### Step 5 — Abstract the threshold notifier behind the backend

**Scope.** Fix leak **L2**. Introduce `NotificationDispatcher` protocol. `LocalNotificationDispatcher` is today's `ThresholdNotifier` (evaluates in-process, fires `UNUserNotification`). `RemoteNotificationDispatcher` (later) consumes `BackendEvent.thresholdCrossed` from the WebSocket and fires the macOS notification.

`ServerStore.ingest` no longer calls `notifier` directly; it emits a "sample ingested" signal that the `LocalBackend` forwards to its dispatcher. The dispatcher's output is folded into `Backend.events()` so `RemoteBackend` can surface the same user-visible events.

**Files touched.** `Sources/System/ThresholdNotifier.swift`, `Sources/State/ServerStore.swift`, `Sources/Backend/LocalBackend.swift`, new `Sources/Backend/NotificationDispatcher.swift`.

**Risk.** Medium. Notifier has subtle state (per-node escalation, snooze). Existing tests should cover regressions.

**Definition of done.** `ServerStore` does not import `ThresholdNotifier`. Notification events flow through `Backend.events()`.

### Step 6 — Sampler auto-update behind the backend

**Scope.** Fix leak **L4**. `SamplerUpdateCoordinator` moves fully inside `LocalBackend`. No UI site holds a reference. In remote mode later, the server owns this — the client only displays version status via a `BackendEvent.samplerVersionChanged`.

**Files touched.** `Sources/Collectors/SamplerUpdateCoordinator.swift` (no code change, just ownership), `Sources/App/AppEnvironment.swift` (drop reference), any UI that shows sampler version (read it off `ServerViewModel` which gets it from samples).

**Risk.** Low.

**Definition of done.** `SamplerUpdateCoordinator` is constructed only inside `LocalBackend`.

---

## After the split — what remote mode adds later

Not part of this plan, but sketching it so the seam targets the right shape.

**Transport.** HTTP + JSON for CRUD and commands, WebSocket for live samples + events. TLS + bearer token. Reconnect with exponential backoff.

**`RemoteBackend` responsibilities.**

- `start()` — connect WebSocket, subscribe to node/sample/event streams, populate observable stores as messages arrive.
- CRUD methods — REST calls; on success, update local observable copy.
- `killProcess()` — REST call, server returns outcome.
- `events()` — passthrough of server events.

**Server endpoints needed (future work).**

| Purpose | Endpoint |
|---|---|
| List/edit nodes | `GET/POST/PUT/DELETE /nodes` |
| Live samples | `WS /stream/samples` |
| History | `GET /nodes/{id}/history?metric=…&range=…` |
| Thresholds (global + per-node) | `GET/PUT /settings/thresholds`, `PUT /nodes/{id}/thresholds` |
| Kill process | `POST /nodes/{id}/kill-process?pid=…` |
| Sampler version + update trigger | `GET /nodes/{id}/sampler-version`, `POST /nodes/{id}/update-sampler` |
| Events (threshold crossings, reachability, version changes) | `WS /stream/events` |
| Snooze / ack an active alert | `POST /alerts/{id}/snooze`, `POST /alerts/{id}/ack` |

**Escalation and snooze state are per-account (user), not per-client device.** All Mac clients logged into the same account share one view of which alerts are active, snoozed, or acknowledged. The server tracks escalation timers and snooze windows; `WS /stream/events` fans the same event stream out to every client subscribed under one account; snooze/ack mutations broadcast the resulting state change back to all of that account's clients.

This means `RemoteNotificationDispatcher` (Step 5) is purely a renderer of server-pushed events — no local escalation timers, no local snooze tracking. Local mode is unaffected: `ThresholdNotifier` keeps its in-process state.

**Stays on the client in both modes.**

- `TerminalLauncher` — opens `ssh://` URLs locally. Works identically if the client has the node's `sshHost`.
- `ClientSettings` — terminal app choice, density, favorites.
- Menu-bar icon rendering, popover UI, window positioning.

**Does not exist on the client in remote mode.**

- `RealCollector`, `SamplerInvoker`, `SamplerUpdateCoordinator`, `SSHBootstrap`, `ProcessRunner` (for remote work), sampler binary bundling.
- Direct SSH anywhere, except `TerminalLauncher`.

---

## Non-goals

- **Building the remote server.** Separate project, separate repo, Go. Not part of this plan.
- **Wire format design.** Deferred until the server work starts. The sampler JSON schema in [`sampler.md`](sampler.md) is already stable and likely informs but does not dictate the client↔server contract.
- **Auth, accounts, multi-tenant.** Phase 4 concerns per [`PLAN.md`](PLAN.md). Remote mode in Phase 3 is "point at a self-hosted server URL + bearer token."
- **Removing local mode.** Both modes ship. Local mode is the zero-config onboarding experience and stays the default.

## Estimated effort

Swift-side only, assuming no new UI work and one engineer:

| Step | Est. |
|---|---|
| 1. Backend protocol + LocalBackend shell | 0.5 day |
| 2. Route process kill through backend | 0.5 day |
| 3. Route node mutations through backend | 1 day |
| 4. Split AppSettings → ClientSettings + ServerSettings | 1–2 days |
| 5. Abstract ThresholdNotifier behind backend | 1 day |
| 6. SamplerUpdateCoordinator ownership move | 0.25 day |
| **Total** | **~5 days** |

Each step is independently reviewable and shippable. The order is not strict — steps 2, 3, 5, 6 are parallelizable after step 1 lands.
