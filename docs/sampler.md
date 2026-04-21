# Towertail Sampler — Spec

A tiny Go binary that runs on each monitored server, reads OS metrics via `gopsutil`, and emits one JSON sample per invocation (one-shot) or one JSON line per interval (streaming). The Mac app bundles all target binaries, pushes the right one to `~/.towertail/towertail-sampler` on first connect, and invokes it over SSH.

This document replaces the earlier `probe.sh` design in [`PLAN.md`](PLAN.md). For the rationale behind choosing an sampler over raw-SSH command parsing, see the session summary in that file's §4 intro.

---

## 1. Goals & non-goals

**Goals.** One uniform sample schema across Linux / macOS / BSD; single round-trip per poll; sub-10MB static binary; no host-side dependencies beyond the binary itself; runs fine as the SSH-authenticated non-root user for the metrics Towertail needs (CPU, mem, disk, net, top-N processes).

**Non-goals for v1.** Root-only collectors that require kernel capabilities the SSH user doesn't have: per-process **I/O** counters across all users, per-process open-fd enumeration across all users, disk SMART, temperature. No on-host daemon, no systemd unit, no listening socket. No plugin system. No metric aggregation on the host — the Mac app decimates and stores.

---

## 2. Binary name & location

- **Binary name:** `towertail-sampler`
- **Install path on remote:** `~/.towertail/towertail-sampler` (symlink or direct)
- **Version/metadata file:** `~/.towertail/towertail-sampler.version` — one line, the SHA-256 of the binary the app uploaded.
- **Working dir on invocation:** `$HOME` — no files written.

The sampler never writes files on the host. All state lives on the Mac side.

---

## 3. Invocation modes

### 3.1 One-shot (default for v1)

```
~/.towertail/towertail-sampler --once
```

Collects one sample, prints one JSON object on stdout, exits 0. Exit non-zero on fatal collection error (unlikely — partial data is preferred over failure).

Used by the app like: `ssh host '~/.towertail/towertail-sampler --once'`. Combined with `ControlMaster` this is one TCP round-trip per poll and matches the current 30-second cadence trivially.

### 3.2 Streaming (M4+ / opt-in)

```
~/.towertail/towertail-sampler --interval 1s
```

Emits one JSON object per tick as newline-delimited JSON (NDJSON) until stdin closes or the process is killed. Same schema as one-shot. Exits 0 on clean EOF.

Used over a persistent SSH channel when the user wants sub-30s updates without per-sample SSH overhead. The Mac app reads NDJSON off stdout and treats each line as an independent `Sample`.

### 3.3 Control flags

| Flag | Default | Purpose |
|---|---|---|
| `--once` | (mode) | Emit one sample and exit. |
| `--interval <dur>` | (mode) | Emit every `<dur>` (e.g. `1s`, `5s`, `30s`) until killed. |
| `--version` | — | Print `towertail-sampler <semver> <sha>` and exit 0. Used by the bootstrap handshake. |
| `--self-check` | — | Collect one sample, throw it away, print `ok` + exit 0. Used to verify the binary runs on the target kernel before the app commits to using it. |
| `--no-disk` / `--no-net` / `--no-proc` | off | Escape hatches if a specific collector hangs on a weird host — Mac-side config disables it for that server. |
| `--top-n <int>` | 20 | Cap on the process list. Returns the union of top-N by CPU% and top-N by RSS, deduped (so you get between N and 2N rows). `0` disables the cap. |

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
    "sampler": "0.1.0+abc1234",
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
  "disk_io": {
    "read_bps":   4194304,
    "write_bps":  1048576,
    "read_cum":   2800479338496,
    "write_cum":  1525421113344
  },
  "net": {
    "rx_bps": 3355443,
    "tx_bps": 838860,
    "rx_cum": 198723849203,
    "tx_cum":  48239874321
  },
  "procs": {
    "root": false,
    "top_n": 20,
    "total": 312,
    "visible": 311,
    "items": [
      { "pid": 812, "ppid": 1, "name": "postgres", "cmd": "postgres: main", "user": "postgres", "cpu_pct": 42.7, "rss": 536870912, "threads": 6, "state": "S", "read_bytes": 58720256, "write_bytes": 12582912 },
      { "pid": 914, "ppid": 1, "name": "node",     "cmd": "node /app/server.js", "user": "app", "cpu_pct": 18.3, "rss": 430080000, "threads": 12, "state": "S" }
    ]
  },
  "errors": []
}
```

### Field notes

- **`ts`**: RFC 3339 with millisecond precision, always UTC (`Z`). The host sets this; the Mac app trusts it for in-sample delta math but uses its own `Date()` for store keys (hosts can drift).
- **`cpu.pct`**: aggregate across cores, 0–100 (not 0–1). Computed as a ~200ms delta inside the sampler so one-shot mode doesn't need prior state.
- **`mem.used`**: `total - available` on Linux, `app + wired + compressed` on Darwin. Excludes cached/inactive so the percentage matches what a human would call "memory in use."
- **`disks`**: one object per mount after filtering `tmpfs`, `devfs`, `devtmpfs`, `overlay`, `map auto_home`, `squashfs`, `autofs`. If `--no-disk` is set, omit the array entirely (not `[]`).
- **`disk_io`**: system-wide aggregate disk I/O summed across physical block devices (partitions are rolled up into their parent device to avoid double-counting). `read_cum` / `write_cum` are lifetime byte counters (same contract as `net.rx_cum` / `tx_cum`); the Mac app recomputes deltas across polls. `read_bps` / `write_bps` are from a short in-sampler delta window so one-shot mode produces a usable rate without prior state. Omitted when `--no-disk` is set.
- **`net.rx_bps` / `tx_bps`**: delta over the ~200ms self-sampling window in one-shot mode; delta over the actual tick interval in streaming mode. Sum across non-loopback interfaces.
- **`net.rx_cum` / `tx_cum`**: lifetime counters. The Mac app can recompute deltas across polls as a cross-check, and detect counter resets (reboots) when `rx_cum` decreases.
- **`errors`**: non-fatal collector errors (e.g., "netstat returned -1 for iface veth0"). The Mac app logs these but still ingests the rest of the sample.
- **`machine_id`**: optional, read-only. `/etc/machine-id` on Linux, `IOPlatformUUID` on Darwin. Omitted when unavailable (containers without `machine-id`, hardened kernels, etc.). The app uses it as a secondary key to detect hostname renames or collisions — the primary key is still the user-configured SSH target.
- **`procs`**: optional per-process table. Omitted when `--no-proc` is set. `root=true` means the sampler ran with euid 0, so the list is comprehensive across users (Linux: full `/proc` visibility; macOS: `kinfo_proc` with other-user fields filled). `root=false` + macOS means the list only contains the SSH user's own processes. `top_n` echoes the requested cap; `total` is the full process count on the host; `visible` is how many the sampler could inspect (lower than `total` when some entries were gated). `items` is the union of top-N by `cpu_pct` and top-N by `rss`, deduped by pid, ordered CPU-desc. `cpu_pct` is computed from a ~200ms self-sampling delta (same window as aggregate CPU) so it matches `top(1)`'s aggregate-across-cores convention (0..100×cores). `rss` is resident set size in bytes. Per-proc `user`, `cmd`, `threads`, `state`, `ppid`, `start_ts` are best-effort and omitted when the kernel denies access.
- **`procs.items[].read_bytes` / `write_bytes`**: lifetime cumulative per-process disk I/O in bytes. Only present when the sampler can read the counters: **Linux** reads `/proc/<pid>/io`, which is mode 0400 and requires either owning the process or `CAP_SYS_PTRACE` (grant once via `sudo setcap cap_sys_ptrace+ep ~/.towertail/towertail-sampler`); rows without the capability will simply omit both fields. **macOS** does not surface per-process I/O via any API gopsutil supports today, so both fields are always omitted there. The Mac app distinguishes `null` (no visibility) from `0` (truly no I/O since start).

Omitted fields for v1: per-CPU breakdown, temperature, GPU, sensors, per-process network bytes (not exposed by Linux or macOS kernels without root + eBPF / private frameworks). Per-process disk I/O **is** included but may be `null` per-row on Linux without `CAP_SYS_PTRACE` and is always `null` on macOS.

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

**Five builds.** Embedded into the Mac app bundle at `Towertail.app/Contents/Resources/samplers/<triple>/towertail-sampler`. Total bundle bloat ≈ 30MB at `-ldflags="-s -w"`; acceptable.

FreeBSD / OpenBSD support is post-v1 — add `freebsd-amd64` when requested. `windows-amd64` is not a goal (Towertail targets Unix hosts).

---

## 6. Bootstrap handshake

Runs once per host, and again whenever the Mac app's bundled sampler version differs from what's installed. Owned by `SamplerBootstrap.swift` on the Mac side.

```
┌─ Mac app ──────────────────────────┐            ┌─ Remote host ────────────┐
│                                    │            │                          │
│ 1. detect arch                     │─── ssh ───▶│ uname -sm → "Linux aarch64" │
│                                    │◀────────── │                          │
│ 2. probe existing                  │─── ssh ───▶│ ~/.towertail/towertail-sampler --version │
│                                    │◀────────── │ (or: command not found)  │
│                                    │            │                          │
│ 3. if missing OR version mismatch: │            │                          │
│    choose Resources/samplers/linux-arm64/towertail-sampler                     │
│                                    │─── scp ───▶│ ~/.towertail/towertail-sampler.new   │
│                                    │─── ssh ───▶│ mkdir -p ~/.towertail &&  │
│                                    │            │ chmod +x ~/.towertail/towertail-sampler.new && │
│                                    │            │ mv -f ~/.towertail/towertail-sampler.new ~/.towertail/towertail-sampler │
│                                    │            │                          │
│ 4. self-check                      │─── ssh ───▶│ ~/.towertail/towertail-sampler --self-check │
│                                    │◀────────── │ ok                       │
│                                    │            │                          │
│ 5. steady state: poll              │─── ssh ───▶│ ~/.towertail/towertail-sampler --once │
│                                    │◀── json ── │                          │
└────────────────────────────────────┘            └──────────────────────────┘
```

**Arch detection.** `uname -sm` → map to one of the five triples. Unknown → fall back to `linux-amd64` and surface "Unsupported host arch — please report" in the card's offline reason.

**Atomic replace.** Write to `sampler.new` then `mv -f` so an in-flight streaming sampler invocation never sees a half-written binary. (Busy `ETXTBSY` on Linux is avoided this way too.)

**Failure modes.**
- `scp` denied (no write to `~/.towertail`): show "Home directory not writable" in the card. Don't retry in a tight loop.
- `--self-check` fails (missing glibc on Alpine, etc.): mark host as `offline(reason: "sampler incompatible")`. Don't retry the upload until user clicks refresh.
- `ETXTBSY` despite atomic replace (old kernel): kill the streaming invocation first, then upload.

---

## 7. Permissions

Running as the SSH-authenticated **non-root** user:

**Works (no privilege needed):**
- `gopsutil/cpu` — aggregate CPU %, load avg, core count (reads `/proc/stat`, `/proc/loadavg` on Linux; `host_statistics` / `sysctl` on Darwin).
- `gopsutil/mem` — total/available/used/swap (reads `/proc/meminfo`; `host_statistics64` on Darwin).
- `gopsutil/disk` — mount list and usage via `statfs`. Mounts the user can `read` on.
- `gopsutil/disk.IOCounters` — aggregate device-level read/write byte counters via `/proc/diskstats` (world-readable on Linux) or IOKit (Darwin, no elevation needed).
- `gopsutil/net` — interface counters via `/proc/net/dev` (world-readable on Linux); `getifaddrs` on Darwin.
- Hostname, uptime, kernel version, arch.
- **Per-process basics** (pid, ppid, name, cmdline, user, CPU%, RSS, threads, state) — `/proc/<pid>/stat` + `/proc/<pid>/cmdline` are world-readable on Linux. On Darwin the sampler sees **only the SSH user's own processes** via `sysctl kinfo_proc` when running non-root — the `procs.root` field in the sample signals which regime is active so the Mac app can show a "running as non-root on macOS" badge.
- **Per-process disk I/O for the SSH user's own processes** — `/proc/<pid>/io` is 0400 but owned by the process's user, so the SSH user sees their own rows without elevation. Other users' rows require `CAP_SYS_PTRACE` (see below). Always omitted on macOS.

**Needs elevated access (auto-detected; sampler degrades gracefully):**
- Per-process **I/O** counters for processes owned by **other** users (`/proc/<pid>/io` is 0400). Granted by `CAP_SYS_PTRACE` on the binary — see §7 bottom. Without it, the sampler still emits the field for the SSH user's own processes and silently omits it for the rest.
- Per-process **open fd** enumeration across other users (`/proc/<pid>/fd` is 0500 user-only).
- `/proc/1/mounts` on some hardened distros — use `/proc/self/mounts` instead. gopsutil does this correctly.
- `/proc/net/sockstat` on kernels with `restricted_net_hostname` — surface in `errors[]`, keep going.
- **macOS**: per-process CPU/mem/cmdline across **other users** requires root (or a signed entitlement + taskgated trust). The sampler auto-detects this via `geteuid() == 0` and sets `procs.root` accordingly so the Mac app can display visibility honestly instead of a misleadingly short list.

**If the user later wants root-gated metrics** (per-proc I/O, cross-user visibility on macOS), the path is `sudo setcap cap_sys_ptrace,cap_dac_read_search+ep ~/.towertail/towertail-sampler` on Linux, or invoking the sampler under `sudo` via SSH. The sampler checks euid at startup and widens the `procs` payload automatically.

---

## 8. Trust & supply chain

- **Open-source.** The sampler source is in `sampler/` of this repo. Reproducible build: `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=<semver> -X main.sha=<sha>" ./cmd/sampler`.
- **Signing.** The sampler binary itself is unsigned (Linux doesn't care; macOS targets don't check because `scp` doesn't set `com.apple.quarantine`). The Mac **app** is Developer ID signed and notarized, so users trust the binary transitively through the app bundle.
- **Fingerprint verification.** After scp, the Mac app runs `shasum -a 256 ~/.towertail/towertail-sampler` and compares against the embedded manifest. Mismatch → abort bootstrap, flag as offline with reason "sampler integrity check failed" and log.
- **No network calls from the sampler.** It reads local counters and writes stdout. It does not resolve DNS, open sockets outbound, or contact any server. Grep the source for `net.Dial` — there should be nothing.

---

## 9. Source layout (inside `sampler/`)

```
sampler/
├── go.mod
├── go.sum
├── cmd/
│   └── sampler/
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

The Mac-side build script (`app/Packaging/build-samplers.sh` or similar) iterates the five triples, runs the Go build, writes `Towertail.app/Contents/Resources/samplers/<triple>/towertail-sampler` + a `manifest.json` with sha256 per triple.

---

## 10. Testing

- **Go unit tests** in `sampler/internal/collect/*_test.go` — deterministic fakes for procfs/sysctl.
- **Integration** via Docker: `sampler/test/linux-amd64.dockerfile` boots Debian slim, drops a known load, runs `sampler --once`, asserts schema and plausible ranges. Same for Alpine (musl) to catch cgo surprises.
- **macOS integration** has to run on the dev machine directly (no darwin-in-docker). A simple `go test -tags=integration` with `runtime.GOOS == "darwin"` gates.
- **Schema contract test** on the Swift side: round-trip decode every `*.json` fixture in `sampler/test/fixtures/` into `Sample` struct; fail loudly on unknown fields so protocol drift is caught at build time.

---

## 11. Versioning & compatibility

- **Sampler version** = semver, injected via `-ldflags -X main.version=v0.1.0`. Plus short SHA: `v0.1.0+abc1234`.
- **Schema version** = top-level `v` integer. Bumped only on breaking changes. The app refuses to parse samples with `v > appKnownV` (future-proof) and gracefully degrades for `v < appKnownV` (backward-compat).
- **Rollout.** When the app ships a new sampler, first-connect triggers bootstrap replace. If the user doesn't open the app for weeks, old sampler keeps running fine — samples still decode. Forced upgrades are never needed for metrics alone; only if the v bumps.
