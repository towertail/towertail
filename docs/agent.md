# Towertail Agent — Spec

A tiny Go binary that runs on each monitored server, reads OS metrics via `gopsutil`, and emits one JSON sample per invocation (one-shot) or one JSON line per interval (streaming). The Mac app bundles all target binaries, pushes the right one to `~/.towertail/agent` on first connect, and invokes it over SSH.

This document replaces the earlier `probe.sh` design in [`PLAN.md`](PLAN.md). For the rationale behind choosing an agent over raw-SSH command parsing, see the session summary in that file's §4 intro.

---

## 1. Goals & non-goals

**Goals.** One uniform sample schema across Linux / macOS / BSD; single round-trip per poll; sub-10MB static binary; no host-side dependencies beyond the binary itself; runs fine as the SSH-authenticated non-root user for the metrics Towertail needs (CPU, mem, disk, net).

**Non-goals for v1.** Root-only collectors (per-process I/O across users, disk SMART, temperature). No on-host daemon, no systemd unit, no listening socket. No plugin system. No metric aggregation on the host — the Mac app decimates and stores.

---

## 2. Binary name & location

- **Binary name:** `towertail-agent`
- **Install path on remote:** `~/.towertail/agent` (symlink or direct)
- **Version/metadata file:** `~/.towertail/agent.version` — one line, the SHA-256 of the binary the app uploaded.
- **Working dir on invocation:** `$HOME` — no files written.

The agent never writes files on the host. All state lives on the Mac side.

---

## 3. Invocation modes

### 3.1 One-shot (default for v1)

```
~/.towertail/agent --once
```

Collects one sample, prints one JSON object on stdout, exits 0. Exit non-zero on fatal collection error (unlikely — partial data is preferred over failure).

Used by the app like: `ssh host '~/.towertail/agent --once'`. Combined with `ControlMaster` this is one TCP round-trip per poll and matches the current 30-second cadence trivially.

### 3.2 Streaming (M4+ / opt-in)

```
~/.towertail/agent --interval 1s
```

Emits one JSON object per tick as newline-delimited JSON (NDJSON) until stdin closes or the process is killed. Same schema as one-shot. Exits 0 on clean EOF.

Used over a persistent SSH channel when the user wants sub-30s updates without per-sample SSH overhead. The Mac app reads NDJSON off stdout and treats each line as an independent `Sample`.

### 3.3 Control flags

| Flag | Default | Purpose |
|---|---|---|
| `--once` | (mode) | Emit one sample and exit. |
| `--interval <dur>` | (mode) | Emit every `<dur>` (e.g. `1s`, `5s`, `30s`) until killed. |
| `--version` | — | Print `towertail-agent <semver> <sha>` and exit 0. Used by the bootstrap handshake. |
| `--self-check` | — | Collect one sample, throw it away, print `ok` + exit 0. Used to verify the binary runs on the target kernel before the app commits to using it. |
| `--no-disk` / `--no-net` / `--no-proc` | off | Escape hatches if a specific collector hangs on a weird host — Mac-side config disables it for that server. |

No other flags for v1. No config file. No env vars beyond what Go reads by default.

---

## 4. Output schema

One JSON object per sample. Newline-delimited in streaming mode. Fields are stable and versioned via the top-level `v` — bump on breaking changes.

```json
{
  "v": 1,
  "ts": "2026-04-20T19:42:07.103Z",
  "host": {
    "name": "db-primary",
    "os": "linux",
    "arch": "arm64",
    "kernel": "6.6.22-1-arm64",
    "uptime_s": 1048273,
    "agent": "0.1.0+abc1234",
    "machine_id": "24c63e84-ed40-5734-bf08-72572b9bea7d"
  },
  "cpu": {
    "pct": 42.3,
    "load_1": 1.24,
    "load_5": 0.98,
    "load_15": 0.81,
    "cores": 8
  },
  "mem": {
    "used": 5814763520,
    "total": 16777216000
  },
  "swap": {
    "used": 0,
    "total": 0
  },
  "disks": [
    { "mount": "/",     "fs": "ext4", "used": 42949672960, "total": 107374182400 },
    { "mount": "/data", "fs": "xfs",  "used": 17179869184, "total": 53687091200 }
  ],
  "net": {
    "rx_bps": 3355443,
    "tx_bps": 838860,
    "rx_cum": 198723849203,
    "tx_cum":  48239874321
  },
  "errors": []
}
```

### Field notes

- **`ts`**: RFC 3339 with millisecond precision, always UTC (`Z`). The host sets this; the Mac app trusts it for in-sample delta math but uses its own `Date()` for store keys (hosts can drift).
- **`cpu.pct`**: aggregate across cores, 0–100 (not 0–1). Computed as a ~200ms delta inside the agent so one-shot mode doesn't need prior state.
- **`mem.used`**: `total - available` on Linux, `app + wired + compressed` on Darwin. Excludes cached/inactive so the percentage matches what a human would call "memory in use."
- **`disks`**: one object per mount after filtering `tmpfs`, `devfs`, `devtmpfs`, `overlay`, `map auto_home`, `squashfs`, `autofs`. If `--no-disk` is set, omit the array entirely (not `[]`).
- **`net.rx_bps` / `tx_bps`**: delta over the ~200ms self-sampling window in one-shot mode; delta over the actual tick interval in streaming mode. Sum across non-loopback interfaces.
- **`net.rx_cum` / `tx_cum`**: lifetime counters. The Mac app can recompute deltas across polls as a cross-check, and detect counter resets (reboots) when `rx_cum` decreases.
- **`errors`**: non-fatal collector errors (e.g., "netstat returned -1 for iface veth0"). The Mac app logs these but still ingests the rest of the sample.
- **`machine_id`**: optional, read-only. `/etc/machine-id` on Linux, `IOPlatformUUID` on Darwin. Omitted when unavailable (containers without `machine-id`, hardened kernels, etc.). The app uses it as a secondary key to detect hostname renames or collisions — the primary key is still the user-configured SSH target.

Omitted fields for v1: per-CPU breakdown, per-process table, temperature, GPU, sensors. All go in `v=2` stretch.

---

## 5. Build matrix

Built from one Go source tree. `go build` for each target. All binaries are pure-Go where possible (cgo off unless gopsutil forces it on a target).

| Target triple | GOOS / GOARCH | Notes |
|---|---|---|
| `linux-amd64` | linux / amd64 | x86_64 servers, EC2 |
| `linux-arm64` | linux / arm64 | Raspberry Pi 4+, Graviton, Ampere |
| `linux-armv7` | linux / arm (GOARM=7) | Raspberry Pi 3, older ARM SBCs |
| `darwin-arm64` | darwin / arm64 | Apple silicon Macs as remote hosts |
| `darwin-amd64` | darwin / amd64 | Intel Macs as remote hosts |

**Five builds.** Embedded into the Mac app bundle at `Towertail.app/Contents/Resources/agents/<triple>/towertail-agent`. Total bundle bloat ≈ 30MB at `-ldflags="-s -w"`; acceptable.

FreeBSD / OpenBSD support is post-v1 — add `freebsd-amd64` when requested. `windows-amd64` is not a goal (Towertail targets Unix hosts).

---

## 6. Bootstrap handshake

Runs once per host, and again whenever the Mac app's bundled agent version differs from what's installed. Owned by `AgentBootstrap.swift` on the Mac side.

```
┌─ Mac app ──────────────────────────┐            ┌─ Remote host ────────────┐
│                                    │            │                          │
│ 1. detect arch                     │─── ssh ───▶│ uname -sm → "Linux aarch64" │
│                                    │◀────────── │                          │
│ 2. probe existing                  │─── ssh ───▶│ ~/.towertail/agent --version │
│                                    │◀────────── │ (or: command not found)  │
│                                    │            │                          │
│ 3. if missing OR version mismatch: │            │                          │
│    choose Resources/agents/linux-arm64/towertail-agent                     │
│                                    │─── scp ───▶│ ~/.towertail/agent.new   │
│                                    │─── ssh ───▶│ mkdir -p ~/.towertail &&  │
│                                    │            │ chmod +x ~/.towertail/agent.new && │
│                                    │            │ mv -f ~/.towertail/agent.new ~/.towertail/agent │
│                                    │            │                          │
│ 4. self-check                      │─── ssh ───▶│ ~/.towertail/agent --self-check │
│                                    │◀────────── │ ok                       │
│                                    │            │                          │
│ 5. steady state: poll              │─── ssh ───▶│ ~/.towertail/agent --once │
│                                    │◀── json ── │                          │
└────────────────────────────────────┘            └──────────────────────────┘
```

**Arch detection.** `uname -sm` → map to one of the five triples. Unknown → fall back to `linux-amd64` and surface "Unsupported host arch — please report" in the card's offline reason.

**Atomic replace.** Write to `agent.new` then `mv -f` so an in-flight streaming agent invocation never sees a half-written binary. (Busy `ETXTBSY` on Linux is avoided this way too.)

**Failure modes.**
- `scp` denied (no write to `~/.towertail`): show "Home directory not writable" in the card. Don't retry in a tight loop.
- `--self-check` fails (missing glibc on Alpine, etc.): mark host as `offline(reason: "agent incompatible")`. Don't retry the upload until user clicks refresh.
- `ETXTBSY` despite atomic replace (old kernel): kill the streaming invocation first, then upload.

---

## 7. Permissions

Running as the SSH-authenticated **non-root** user:

**Works (no privilege needed):**
- `gopsutil/cpu` — aggregate CPU %, load avg, core count (reads `/proc/stat`, `/proc/loadavg` on Linux; `host_statistics` / `sysctl` on Darwin).
- `gopsutil/mem` — total/available/used/swap (reads `/proc/meminfo`; `host_statistics64` on Darwin).
- `gopsutil/disk` — mount list and usage via `statfs`. Mounts the user can `read` on.
- `gopsutil/net` — interface counters via `/proc/net/dev` (world-readable on Linux); `getifaddrs` on Darwin.
- Hostname, uptime, kernel version, arch.

**Doesn't work without root (explicitly out of scope for v1):**
- Per-process I/O for processes owned by other users (`/proc/<pid>/io` is 0400 root on Linux).
- `/proc/1/mounts` on some hardened distros — use `/proc/self/mounts` instead. gopsutil does this correctly.
- `/proc/net/sockstat` on kernels with `restricted_net_hostname` — surface in `errors[]`, keep going.
- macOS: per-process CPU across all users needs root. v1 doesn't collect per-process metrics, so N/A.

**If the user later wants root-only metrics**, the path is `sudo setcap cap_sys_ptrace,cap_dac_read_search+ep ~/.towertail/agent` on Linux — the agent detects the capability at startup and lights up extra fields. Not wired in v1.

---

## 8. Trust & supply chain

- **Open-source.** The agent source is in `agent/` of this repo. Reproducible build: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=<semver> -X main.sha=<sha>" ./cmd/agent`.
- **Signing.** The agent binary itself is unsigned (Linux doesn't care; macOS targets don't check because `scp` doesn't set `com.apple.quarantine`). The Mac **app** is Developer ID signed and notarized, so users trust the binary transitively through the app bundle.
- **Fingerprint verification.** After scp, the Mac app runs `shasum -a 256 ~/.towertail/agent` and compares against the embedded manifest. Mismatch → abort bootstrap, flag as offline with reason "agent integrity check failed" and log.
- **No network calls from the agent.** It reads local counters and writes stdout. It does not resolve DNS, open sockets outbound, or contact any server. Grep the source for `net.Dial` — there should be nothing.

---

## 9. Source layout (inside `agent/`)

```
agent/
├── go.mod
├── go.sum
├── cmd/
│   └── agent/
│       └── main.go              # flag parsing, mode dispatch, JSON emit
├── internal/
│   ├── collect/
│   │   ├── cpu.go               # gopsutil wrapping + delta math
│   │   ├── mem.go
│   │   ├── disk.go              # mount filter list
│   │   ├── net.go               # interface filter (skip lo*, docker*, veth*)
│   │   └── host.go              # hostname, kernel, uptime, arch
│   ├── schema/
│   │   └── sample.go            # Sample struct + JSON marshaling
│   └── version/
│       └── version.go           # ldflags-injected version + sha
├── Makefile                     # build-all, build-<triple>
└── README.md
```

The Mac-side build script (`app/Packaging/build-agents.sh` or similar) iterates the five triples, runs the Go build, writes `Towertail.app/Contents/Resources/agents/<triple>/towertail-agent` + a `manifest.json` with sha256 per triple.

---

## 10. Testing

- **Go unit tests** in `agent/internal/collect/*_test.go` — deterministic fakes for procfs/sysctl.
- **Integration** via Docker: `agent/test/linux-amd64.dockerfile` boots Debian slim, drops a known load, runs `agent --once`, asserts schema and plausible ranges. Same for Alpine (musl) to catch cgo surprises.
- **macOS integration** has to run on the dev machine directly (no darwin-in-docker). A simple `go test -tags=integration` with `runtime.GOOS == "darwin"` gates.
- **Schema contract test** on the Swift side: round-trip decode every `*.json` fixture in `agent/test/fixtures/` into `Sample` struct; fail loudly on unknown fields so protocol drift is caught at build time.

---

## 11. Versioning & compatibility

- **Agent version** = semver, injected via `-ldflags -X main.version=v0.1.0`. Plus short SHA: `v0.1.0+abc1234`.
- **Schema version** = top-level `v` integer. Bumped only on breaking changes. The app refuses to parse samples with `v > appKnownV` (future-proof) and gracefully degrades for `v < appKnownV` (backward-compat).
- **Rollout.** When the app ships a new agent, first-connect triggers bootstrap replace. If the user doesn't open the app for weeks, old agent keeps running fine — samples still decode. Forced upgrades are never needed for metrics alone; only if the v bumps.
