# Towertail Backend Research: Data Collection & Storage

Blueprint for the backend layer of Towertail — a SwiftUI menu-bar app polling remote servers over Tailscale SSH at 10-second cadence, rendering per-server cards with CPU / memory / disk / network and sparklines over a rolling 24-hour window.

## 1. Remote metrics collection via SSH

### 1.1 The command menagerie

| Metric      | Linux (Ubuntu/Debian)                                         | macOS (Darwin)                                       |
| ----------- | ------------------------------------------------------------- | ---------------------------------------------------- |
| CPU %       | `/proc/stat` (two-sample delta) or `top -bn1`                 | `top -l 1 -n 0` (reads `CPU usage:` line)            |
| Memory      | `/proc/meminfo` (`MemTotal`, `MemAvailable`)                  | `vm_stat` + `sysctl hw.memsize`, or `top -l 1`       |
| Disk        | `df -Pk` (POSIX, portable)                                    | `df -Pk` (same)                                      |
| Network     | `/proc/net/dev` (two-sample delta)                            | `netstat -ibn` (two-sample delta)                    |

Pitfalls:

- `top -bn1` Linux output varies across coreutils/busybox — avoid for CPU. `top -l 1` on macOS already averages over its refresh interval.
- `vmstat 1 2`: valid (second row is delta) but extra round-trip, BusyBox column layout differs.
- `iostat`, `sar` need `sysstat` — don't assume installed. `ioreg` is overkill.
- `df -P` (POSIX) normalizes output across BSD/GNU.

### 1.2 Recommendation: ship a small shell script on first connect

Option (a) wins. On first connect, push `probe.sh` via `ssh host 'cat > ~/.towertail/probe.sh && chmod +x ...'`. Each poll runs `sh ~/.towertail/probe.sh`.

Why not (b) detect-and-dispatch from Swift? Costs two round-trips or a fat inline heredoc every poll. Ship once, version-tag, redeploy on mismatch.

Why not (c) a helper daemon? Too invasive — users don't want to `sudo` on 20 Tailscale hosts. Keep host-side deps to `sh`, `awk`, `df`.

The script emits one line of `key=value` pairs (more robust than JSON without `jq`). OS is detected via `uname -s`:

```sh
#!/bin/sh
# ~/.towertail/probe.sh — emits one line: cpu=... mem_used=... mem_total=... ...
set -eu
OS=$(uname -s)
now=$(date +%s)

if [ "$OS" = "Linux" ]; then
  # CPU: two /proc/stat samples, 200ms apart
  read _ u1 n1 s1 i1 w1 rest < /proc/stat
  sleep 0.2
  read _ u2 n2 s2 i2 w2 rest < /proc/stat
  busy=$(( (u2+n2+s2) - (u1+n1+s1) ))
  total=$(( busy + (i2+w2) - (i1+w1) ))
  cpu=$(awk -v b="$busy" -v t="$total" 'BEGIN{ printf "%.2f", (t>0)? 100.0*b/t : 0 }')

  mem_total=$(awk '/MemTotal:/ {print $2*1024}' /proc/meminfo)
  mem_avail=$(awk '/MemAvailable:/ {print $2*1024}' /proc/meminfo)
  mem_used=$(( mem_total - mem_avail ))

  # Network: per-interface rx/tx bytes. Mac-side filters virtual/container NICs
  # (see §1.7); we emit everything we see so user toggles don't need a re-probe.
  nics_json=$(awk -F'[: ]+' 'NR>2 && $2 != "" {
      printf "{\"name\":\"%s\",\"rx\":%d,\"tx\":%d},", $2, $3, $11
  }' /proc/net/dev)
  nics_json="[${nics_json%,}]"
else
  # Darwin
  cpu=$(top -l 1 -n 0 | awk '/CPU usage/ {gsub("%",""); print $3+$5}')
  page_size=$(sysctl -n hw.pagesize)
  mem_total=$(sysctl -n hw.memsize)
  mem_used=$(vm_stat | awk -v p="$page_size" '
    /Pages active/       {a=$3}
    /Pages wired down/   {w=$4}
    /Pages occupied by compressor/ {c=$5}
    END { gsub("\\.","",a); gsub("\\.","",w); gsub("\\.","",c); print (a+w+c)*p }')
  nics_json=$(netstat -ibn | awk '$1 !~ /Name/ && !seen[$1]++ {
      printf "{\"name\":\"%s\",\"rx\":%d,\"tx\":%d},", $1, $7, $10
  }')
  nics_json="[${nics_json%,}]"
fi

# Disk: one line per real mount, JSON-ish array. Filter out pseudo/virtual FS
# and anything smaller than 1GB (snap loops, efi stubs) — see §1.6.
disks=$(df -Pk 2>/dev/null)
disk_json=$(echo "$disks" | awk 'NR>1 {
    dev=$1; total_kb=$2; used_kb=$3; mount=$6
    # device / FS type denylist
    if (dev ~ /^(tmpfs|devtmpfs|devfs|proc|sysfs|cgroup|overlay|squashfs|autofs|fusectl|map|none)/) next
    # mount prefix denylist
    if (mount ~ /^\/(snap|run|dev|sys|proc)($|\/)/) next
    if (mount ~ /^\/var\/lib\/docker\/overlay2/) next
    # size floor: skip < 1 GiB
    if (total_kb < 1048576) next
    printf "{\"mount\":\"%s\",\"used\":%d,\"total\":%d},", mount, used_kb*1024, total_kb*1024
}')
disk_json="[${disk_json%,}]"

printf 'ts=%s host_id=%s hostname=%s cpu=%s mem_used=%s mem_total=%s disks=%s nics=%s\n' \
  "$now" "$host_id" "$hostname" "$cpu" "$mem_used" "$mem_total" "$disk_json" "$nics_json"
```

The `host_id` and `hostname` are resolved at the top of the script — see §1.5 below.

### 1.5 Host identity: stable ID independent of hostname/IP

The Mac doesn't use IP (can change with DHCP/VPN reconnects) or hostname (user can rename) as the internal key. It uses a **machine ID** resolved by the probe, with a fallback.

**Primary: read an OS-managed hardware/machine identifier (no writes to host).**

- **Linux:** `/etc/machine-id` — 32 hex chars, set once at first boot by systemd. Stable across hostname renames, network moves, and reboots. Regenerated only on fresh OS installs or explicit wipe.
- **Darwin:** `ioreg -rd1 -c IOPlatformExpertDevice | awk -F'"' '/IOPlatformUUID/{print $4}'` — tied to the logic board. Stable for the lifetime of the hardware.

**Fallback: generate and persist `~/.towertail/host-id`** if neither is available (minimal containers without systemd, some BSDs, restrictive chroots). One-time `uuidgen > ~/.towertail/host-id` guarded by a presence check.

Probe snippet (runs before the metric collection block):

```sh
mkdir -p ~/.towertail
if [ -r /etc/machine-id ]; then
  host_id=$(cat /etc/machine-id)
elif command -v ioreg >/dev/null 2>&1; then
  host_id=$(ioreg -rd1 -c IOPlatformExpertDevice | awk -F'"' '/IOPlatformUUID/{print $4}')
fi
if [ -z "${host_id:-}" ]; then
  if [ ! -s ~/.towertail/host-id ]; then
    # uuidgen is present on both Linux (util-linux) and Darwin; fallback to /proc/sys/kernel/random/uuid
    (command -v uuidgen >/dev/null 2>&1 && uuidgen || cat /proc/sys/kernel/random/uuid) > ~/.towertail/host-id
  fi
  host_id=$(cat ~/.towertail/host-id)
fi
hostname=$(hostname 2>/dev/null || cat /etc/hostname 2>/dev/null || echo unknown)
```

**Tying it together on the Mac side.** `host_id` is the identity key; `hostname` is mutable display metadata. The `host` table stores both — on every probe, match the row by `machine_id`, and update the `hostname` column if it drifted. History stays intact across renames; the user sees the new name automatically.

Edge cases handled by this model:
- **Cloned VMs with duplicate hostnames** — distinct `machine_id`s disambiguate them.
- **Cloned VMs with duplicate `machine_id`** (rare but real — clone without re-running `systemd-machine-id-setup`) — composite uniqueness on `(machine_id, connection_string)` catches it; we treat them as separate hosts keyed by how the user reaches them.
- **OS reinstall** — `machine_id` changes, so the host appears as new. Acceptable; history from the old install is orphaned, not lost. User can manually merge if desired (future feature).
- **Hardware swap** — same as reinstall.

### 1.3 CPU % from `/proc/stat`

`/proc/stat` exports cumulative jiffies since boot — a rate needs two samples. The script samples 200ms apart:

```
busy  = Δ(user + nice + system)
total = busy + Δ(idle + iowait)
cpu%  = 100 * busy / total
```

Alternative: drop the `sleep`, emit the raw counters, and let Swift compute deltas across polls (gives a true between-polls average). At 30s cadence either works; in-script is simpler because you don't persist raw counters.

### 1.4 Network throughput

Same pattern — cumulative bytes. Swift stores last `(rx, tx, ts)` per host:

```
rx_bps = (rx_now - rx_prev) / (ts_now - ts_prev)
```

Guard against counter wraparound and interface resets: if `rx_now < rx_prev`, emit null (not a negative).

### 1.6 Multi-disk hosts: what to collect, what to surface

A real server has many mountpoints. `df -Pk` on a modest box already emits `/`, `/boot`, `/boot/efi`, `/data`, `/var/lib/docker`, plus whatever `tmpfs`/`overlay`/snap-loop churn. The goals split cleanly:

**Collection** — store every *real* mount per probe. One row per `(host_id, ts, mount)` in `disk_sample` (already the schema). Don't roll up on the remote; filtering is a display concern and cheap enough to do on the Mac.

**Probe-side filtering (hard exclusions, never stored).** These aren't disks, they're noise:

- Filesystem types: `tmpfs`, `devtmpfs`, `devfs`, `proc`, `sysfs`, `cgroup*`, `overlay`, `squashfs`, `autofs`, `fusectl`.
- Mount prefixes: `/snap/`, `/var/lib/docker/overlay2/`, `/run/`, `/dev/`, `/sys/`.
- Anything under 1 GB total (snap loops, efi stub partitions on some configs).

The remote `probe.sh` applies the type/prefix filter (via `df -Pk` output + a denylist awk clause) and the <1GB filter, since there's no point paying bandwidth to ship mounts we'll never render.

**User exclusions (soft, configurable).** On top of the hard filter, the user can exclude specific mounts per host (see §6 for settings). User-excluded mounts are still *collected* (so toggling the setting doesn't create gaps in history) but hidden in the UI and ignored by alerts. Rationale: excluding a large data volume they don't care about shouldn't also blind alerts for `/` — they already get that.

**Aggregation for the compact card — `max(used/total)` across included mounts, not average.**

Averaging hides outages. If `/` is at 95% and `/data` is at 20%, the mean (57%) looks nominal while the host is minutes from unable-to-write. The card's single "disk %" number is always the **worst** included mount; the threshold color is computed from that same value. Hover tooltip on the card sparkline reads e.g. `3 disks · worst: / 95%`.

**Per-mount alerts, not per-host.** Alert rules fire at `(host_id, mount)` granularity so the notification text can say `/data on prod-1 at 92%` rather than a useless `prod-1 disk at 92%`. `alert_state` PK extends accordingly:

```sql
CREATE TABLE alert_state (
    host_id       TEXT NOT NULL,
    mount         TEXT,              -- NULL for host-level rules (CPU, mem, net); set for disk rules
    rule          TEXT NOT NULL,
    fired_at      INTEGER,
    last_notified INTEGER,
    PRIMARY KEY (host_id, mount, rule)
);
```

(This supersedes the §4.3 definition — update when implementing.)

**Maximized disk view (in the UI, detailed here for collection implications).** The expanded panel shows one line chart at a time with a dropdown under the "Disk" title listing every *included* mount, sorted worst-first (`used/total` desc), each row labeled `<mount> — <current %>`. Default selection is whichever mount triggered the currently-firing alert, or the worst mount if none are firing. No "combined / average" entry — it isn't actionable. See `research-frontend.md` §9 for the control surface.

**Impact on the ring buffer.** Per §5.6 we keep an in-memory `ContiguousArray<Sample>` per host per metric. Disk needs one ring *per mount*, not one per host, so the expanded chart can render any mount without a SQLite query. With ~5 real mounts per host × 20 hosts × 8,640 samples × ~24 B ≈ **~20MB added** to the ~16MB figure. Still cheap.

### 1.7 Multi-interface hosts: what to aggregate, what to surface

Hosts have more NICs than you'd think. A plain Ubuntu box with Docker installed already reports `eth0`, `lo`, `docker0`, `br-xxx...` (per compose project), `veth*` pairs (per container), and maybe `tailscale0`. Naively summing `/proc/net/dev` double-counts container-to-container chatter as host network activity, making every CPU spike look like a network spike too.

**Goal: default aggregate = real external NICs only.** Loopback and container plumbing never contribute to the card's NET number. Tailscale's TUN counts — for a Tailscale-monitored fleet it's often the only interesting interface.

**Probe-side filtering (hard exclusions, never attributed to the aggregate).**

- **Linux kernel truth:** `/sys/class/net/<if>` symlinks tell you directly whether an interface is virtual. Real NICs resolve under `/sys/devices/pci*`, USB, SoC buses. Fake ones resolve under `/sys/devices/virtual/`. This is the right predicate — don't pattern-match on names.

  ```sh
  for if in /sys/class/net/*; do
      name=$(basename "$if")
      dev=$(readlink -f "$if/device" 2>/dev/null || readlink -f "$if")
      case "$dev" in
          */devices/virtual/*)
              # virtual: only keep tailscale0 / wg* (TUN/TAP that carry real traffic)
              case "$name" in
                  tailscale*|wg*) is_real=1 ;;
                  *)              is_real=0 ;;
              esac ;;
          *) is_real=1 ;;
      esac
      [ "$is_real" = 1 ] || continue
      # read rx/tx from /proc/net/dev for $name
  done
  ```

- **Darwin:** `networksetup -listallhardwareports` enumerates real hardware ports (Ethernet, Wi-Fi, Thunderbolt Bridge). Everything else — `utun*`, `awdl*`, `llw*`, `bridge*`, `ap*`, `anpi*` — is virtual and excluded by default. Tailscale on macOS runs on a `utun` device; add it by matching against `scutil --nwi` or falling back to the `utun` that has traffic (heuristic: include the highest-numbered `utun` with non-zero bytes). Simpler: read `tailscale status --json` on the *Mac* side and pass the peer's Tailscale interface name through to the probe — but that's complex for marginal value. Easier rule for v1: Darwin servers are rare in the target audience; default to physical-only, let the user check a box to include a specific `utun` per host.

- **Always excluded by name (belt-and-suspenders for Linux when `/sys` isn't conclusive):** `lo`, `docker*`, `br-*`, `veth*`, `cni*`, `flannel*`, `cali*`, `virbr*`, `vnet*`, `vmnet*`.

The probe emits per-interface counters for *every* interface seen (not just the aggregated ones) so user-side configuration changes don't require a probe round-trip to materialize history for a newly-included interface. Aggregation happens on the Mac.

Revised probe output — instead of a single `rx=... tx=...` pair, a JSON array like the disk one:

```
nics=[{"name":"eth0","rx":12345678,"tx":98765},{"name":"tailscale0","rx":42,"tx":17},{"name":"docker0","rx":...}]
```

The Mac-side parser picks up the `rx`/`tx` per interface, computes per-interface `rx_bps`/`tx_bps` from consecutive samples, and then aggregates across *included* interfaces for the card.

**Schema addition** (supplements §4.3):

```sql
CREATE TABLE net_sample (
    host_id      TEXT NOT NULL,
    ts           INTEGER NOT NULL,
    interface    TEXT NOT NULL,
    rx_bps       INTEGER,
    tx_bps       INTEGER,
    rx_cum       INTEGER,       -- raw cumulative for next-delta calc
    tx_cum       INTEGER,
    PRIMARY KEY (host_id, ts, interface),
    FOREIGN KEY (host_id, ts) REFERENCES sample(host_id, ts) ON DELETE CASCADE
) WITHOUT ROWID;
```

This replaces `sample.net_rx_bps` / `sample.net_tx_bps` / `sample.rx_cum` / `sample.tx_cum` — drop those columns. The card's aggregate is computed at read time from `net_sample`, not materialized. Rationale: writing both pre-aggregated *and* per-interface would double-store, and if the user toggles an interface's include flag we'd need to re-materialize history. Reading+summing at query time is trivial at this scale (20 hosts × ~4 NICs × 8,640 samples/day).

**User exclusions** live at `network.excluded_interfaces[<host_id>]` in `settings.json` (see §6.1 update below). Same semantics as disk exclusions: collected but not aggregated, no history loss on toggle, alerts ignore excluded interfaces.

**Alerts are on the aggregate, not per-interface.** Unlike disk (where `/` at 95% doesn't care about `/data` at 20%), a network threshold is really "is this host's uplink saturated?" — which is a sum across included NICs. Per-interface alerts would be noisy and rarely actionable. Rule keys stay host-scoped (`mount = NULL` for network rules in `alert_state`).

**Ring buffer impact.** One ring per interface per host, same pattern as disk-per-mount. ~4 real interfaces per host (after filtering) × 20 hosts × 8,640 samples × ~32 B (two channels: rx and tx) ≈ **~20 MB added**. Plus the card renders the *aggregate* sparkline from a derived ring that re-sums on each sample append — O(N) where N is the count of included interfaces per host, negligible.

---

## 2. Swift SSH client

| Option                               | Pros                                                                                  | Cons                                                                                                   |
| ------------------------------------ | ------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| **SwiftNIO SSH** (apple/swift-nio-ssh) | First-party, async/await, modern                                                      | Low-level — you build auth, channel mgmt, keepalives yourself. No OpenSSH config / sampler integration.  |
| **Citadel**                            | Higher-level API over SwiftNIO SSH                                                  | Smaller community; doesn't read `~/.ssh/config`; limited Tailscale-aware behaviour.                    |
| **Shell out to `/usr/bin/ssh`**        | Zero integration cost. Respects `~/.ssh/config`, `ssh-sampler`, `ControlMaster`, Tailscale's MagicDNS. Certs, jump hosts, `known_hosts`, 2FA prompts all Just Work. | Process-per-poll overhead unless you use `ControlMaster`. Parsing stderr is fiddly.                    |

**Recommendation: shell out to `/usr/bin/ssh` with `ControlMaster=auto` + `ControlPersist=10m`.**

For Tailscale, `ssh user@hostname.tailnet.ts.net` via system SSH is the path of least resistance — ssh-sampler, keys, certs, `known_hosts` all Just Work. A pure-Swift client has to reimplement that stack. `ControlMaster` multiplexes over one persistent TCP connection per host; subsequent polls skip the handshake and finish in <50ms.

Config in Application Support:

```
~/.towertail/ssh/config
    Host *.ts.net
        ControlMaster auto
        ControlPath   ~/.towertail/ssh/cm-%r@%h:%p
        ControlPersist 10m
        ServerAliveInterval 30
        ServerAliveCountMax 3
        ConnectTimeout 5
        StrictHostKeyChecking accept-new
        UserKnownHostsFile ~/.towertail/ssh/known_hosts
```

Swift-side invocation sketch:

```swift
func poll(host: String, user: String) async throws -> Sample {
    let proc = Process()
    proc.executableURL = URL(fileURLWithPath: "/usr/bin/ssh")
    proc.arguments = [
        "-F", sshConfigPath, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
        "\(user)@\(host)", "sh ~/.towertail/probe.sh"
    ]
    let stdout = Pipe(); proc.standardOutput = stdout
    let stderr = Pipe(); proc.standardError  = stderr
    try proc.run()
    let data = try stdout.fileHandleForReading.readToEnd() ?? Data()
    proc.waitUntilExit()
    guard proc.terminationStatus == 0 else { throw SSHError.remote(proc.terminationStatus) }
    return try Sample.parse(data)
}
```

Wrap in an `actor` per host so at most one probe is in flight per server.

**Sandboxing:** shelling out rules out the App Sandbox, so plan for Developer ID signing + notarization (like Stats), not Mac App Store distribution.

---

## 3. Tailscale integration

Locate the CLI in order: `/Applications/Tailscale.app/Contents/MacOS/Tailscale` (App Store), `/usr/local/bin/tailscale`, `/opt/homebrew/bin/tailscale`, then `PATH`.

`tailscale status --json`:

```json
{
  "Self":  { "HostName": "fritz-mac", "DNSName": "fritz-mac.tail-scale.ts.net." },
  "Peer":  {
     "nodekey:abc...": {
        "HostName":     "bigbox",
        "DNSName":      "bigbox.tail-scale.ts.net.",
        "OS":           "linux",
        "TailscaleIPs": ["100.64.1.23"],
        "Online":       true,
        "LastSeen":     "2026-04-20T17:21:04Z",
        "Tags":         ["tag:server"]
     }, ...
  }
}
```

Parse into `Codable` structs. Use `DNSName` (without trailing dot) as the SSH target — MagicDNS resolves it inside the tailnet. Optionally filter by tag (`tag:server`).

```swift
struct TSStatus: Decodable {
    struct Peer: Decodable {
        let HostName, DNSName, OS: String
        let Online: Bool
        let Tags: [String]?
    }
    let Peer: [String: Peer]
}
let data = try await run("/usr/local/bin/tailscale", ["status", "--json"])
let peers = try JSONDecoder().decode(TSStatus.self, from: data).Peer.values
    .filter { $0.Online && ($0.Tags?.contains("tag:server") ?? true) }
```

Re-poll `tailscale status --json` every 30s to refresh the host list — no network cost, the CLI talks to local `tailscaled`.

---

## 4. Time-series storage

### 4.1 Sizing

At **10-second cadence** and a **24-hour retention window**, 20 hosts × (86400 / 10) samples/day × 1 day = **172,800 rows**. At ~120 bytes/row = **~20 MB**. Small, fast, trivially housekept.

(We considered 7 days but the cards/popover only ever show recent behavior; a day is enough to reason about "what happened today" without bloating storage. If the user later wants longer history, rollups — 1-min buckets for days 2–7 — are a cheap follow-up.)

### 4.2 Options

- **Core Data**: overkill, painful migrations, no clean SQL escape hatch.
- **Flat files** (CSV ring buffer): fast appends, awful for range queries.
- **Embedded TSDB**: too heavy, no mature macOS story.
- **SQLite via GRDB**: mature, fast, WAL, easy migrations, idiomatic Swift.

**Recommendation: SQLite + GRDB.**

### 4.3 Proposed schema

```sql
CREATE TABLE host (
    id                TEXT PRIMARY KEY,         -- internal UUID (generated on Mac at first insert)
    machine_id        TEXT NOT NULL UNIQUE,     -- from probe: /etc/machine-id, IOPlatformUUID, or ~/.towertail/host-id
    hostname          TEXT NOT NULL,            -- mutable; updated on each probe if it drifted
    connection_string TEXT NOT NULL,            -- user@host:port the user entered (SSH target)
    dns_name          TEXT,                     -- Tailscale MagicDNS name, if applicable
    os                TEXT,
    added_at          INTEGER NOT NULL,
    display_name      TEXT                      -- user override for card title; falls back to hostname
);

CREATE TABLE sample (
    host_id      TEXT NOT NULL REFERENCES host(id) ON DELETE CASCADE,
    ts           INTEGER NOT NULL,          -- unix seconds
    cpu_pct      REAL,                      -- 0..100
    mem_used     INTEGER,                   -- bytes
    mem_total    INTEGER,
    PRIMARY KEY (host_id, ts)
) WITHOUT ROWID;
-- Per-interface network rows live in net_sample; see §1.7. Per-mount disk
-- rows live in disk_sample; see §1.6. The card aggregates at read time.

CREATE TABLE disk_sample (
    host_id      TEXT NOT NULL,
    ts           INTEGER NOT NULL,
    mount        TEXT NOT NULL,
    used         INTEGER,
    total        INTEGER,
    PRIMARY KEY (host_id, ts, mount),
    FOREIGN KEY (host_id, ts) REFERENCES sample(host_id, ts) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX idx_sample_ts ON sample(ts);

-- alert bookkeeping. `mount` is NULL for host-level rules (CPU/mem/net)
-- and set for per-mount disk rules so notifications can name the mount.
CREATE TABLE alert_state (
    host_id       TEXT NOT NULL,
    mount         TEXT,                 -- NULL for host-scoped rules
    rule          TEXT NOT NULL,        -- e.g. "cpu_gt_90_for_5m", "disk_gt_90"
    fired_at      INTEGER,
    last_notified INTEGER,
    PRIMARY KEY (host_id, mount, rule)
);
```

`WITHOUT ROWID` + composite PK covers `(host_id, ts)` range scans — the sparkline query. Use `PRAGMA journal_mode=WAL` and `PRAGMA synchronous=NORMAL`.

```sql
SELECT ts, cpu_pct FROM sample
 WHERE host_id = ? AND ts >= strftime('%s','now') - 3600
 ORDER BY ts;
```

---

## 5. Sampling cadence & back-pressure

**Interval: 10 seconds.** Each sparkline pixel = one 10s sample; the hover tooltip maps 1:1 to a real datapoint. Tradeoffs vs. the earlier 30s proposal:

- **Bandwidth:** ~1KB round-trip × 20 hosts × 6/min = **~170 MB/day**. Fine on home/office links; flag for metered networks.
- **Remote CPU cost:** `probe.sh` spends ~250ms (`sleep 0.2` for the CPU delta) per probe → ~2.5% duty cycle on the probed host. Acceptable on servers; heavy on Pi-class boxes (we can offer a 30s tier per host later).
- **Storage:** see §4.1 — ~20MB for 20 hosts × 24 hours.
- **Notification debouncing becomes mandatory.** A single 10s spike shouldn't page anyone. Alert rules trigger only when N consecutive samples breach the threshold (e.g. CPU > 90% for 3 samples = 30s sustained).

Jitter ±0–2s per-host so 20 hosts don't thunder-herd every 10s boundary. Use `Task { while !Task.isCancelled { try await Task.sleep(for: .seconds(10) + jitter); poll() } }` inside the per-host actor.

**Back-pressure:**

- **One in-flight probe per host, ever.** Actor-per-host enforces this. If the previous hasn't returned, *skip* the tick — don't queue.
- **SSH failure → exponential backoff.** 30s → 60s → 2m → 5m → 10m cap. Reset on first success.
- **Don't retry inside a tick.** Next scheduled poll is the retry. Surface the last error for the card UI.
- **Global concurrency cap** (e.g. 8) so first-sync doesn't saturate the local NIC.

**Retention: 24 hours.** Housekeeping runs every 10 minutes (not hourly — shorter window means we want tighter purge cadence so the DB doesn't balloon past the window between runs):

```sql
DELETE FROM sample WHERE ts < strftime('%s','now') - 86400;
```

`disk_sample` cascades via FK. Rely on `PRAGMA auto_vacuum=INCREMENTAL` plus periodic `PRAGMA incremental_vacuum` after purge.

Future (if longer history is ever requested): tiered rollups — keep raw 10s for 24h, 1-min rollups for days 2–7. Not needed now.

---

## 5.5 Connection strategy: persistent SSH via `ControlMaster`

**Connections are kept open, not re-dialed every 10s.** This is non-negotiable at 10s cadence — fresh TCP + SSH handshakes would cost 500–2000ms each, saturating CPU and the network on first-sync.

We get persistence for free from OpenSSH (no Swift connection pool needed):

1. **First probe** opens the connection and creates a Unix socket at `~/.towertail/ssh/cm-%r@%h:%p` (see config in §2).
2. **Subsequent probes** detect the socket and multiplex a new channel over the existing connection — no handshake, no auth, <50ms per probe.
3. **`ControlPersist 10m`** keeps the master alive for 10 minutes of idle. At 10s cadence we're never idle, so the connection lives indefinitely.
4. **`ServerAliveInterval 30` + `ServerAliveCountMax 3`** detects dead peers within ~90s. On failure, the master exits, and the next probe re-establishes cleanly.

Why **not** one long-lived `ssh host 'while true; do probe.sh; sleep 10; done'` stream?
- Need a framing protocol (JSONL works, but now we own stream state).
- A single dropped connection loses the stream; with per-poll invocation a transient failure only costs one sample.
- Cadence changes (foreground/background tiering in a future version) require restarting the remote loop.
- `ControlMaster` already gives us the performance win.

Why **not** a pure-Swift SSH client (SwiftNIO SSH / Citadel) with a held connection?
- Have to reimplement `~/.ssh/config`, sampler, certs, MagicDNS, `known_hosts`, jump hosts — see §2 tradeoff table.

---

## 5.6 In-memory ring buffer (the hover-tooltip read path)

The SQLite table is the **durable** store (24h). The UI reads from an **in-memory ring buffer** that lives on the `ServerViewModel`:

- **Size:** with 24h retention, the ring can hold the full window: 8,640 samples × 24 bytes/sample (`ts: Int64 + value: Double + flags`) ≈ 200KB per metric per host. 20 hosts × 4 metrics = **~16MB resident**. Still cheap — keep it all in memory, no need for a short 2h window.
- **Write path:** every 10s probe appends one `Sample` to the ring for its host (`ContiguousArray<Sample>` capped at 8,640; drop the oldest when full). Same append goes to SQLite on a background write queue.
- **Read path:** `Sparkline` and hover tooltip read directly from the ring — **no SQLite roundtrip on hover**. `@Observable` fine-grained tracking means only the affected card re-renders when the ring mutates.
- **Cold start:** load the last 24h from SQLite on app launch to pre-fill the ring (`SELECT ... WHERE ts >= now - 86400 ORDER BY ts`), so the UI isn't empty while the first 10s probe is in flight.

**Hover interaction detail** (consumed by the frontend doc):

1. Cursor enters sparkline → `SpatialTapGesture` + `ChartProxy.value(atX:)` maps pixel X to a timestamp.
2. ViewModel exposes a `hoverSample: Sample?` that's the nearest-point match from the ring.
3. The card's big number binds to `hoverSample?.value ?? latestSample.value` — when the cursor leaves, `hoverSample` resets to nil and the display snaps back to "now".
4. No debouncing needed; the ring is already in memory.

The ring buffer is populated at app launch with the last 24h from SQLite (`SELECT ... WHERE ts >= now - 86400 ORDER BY ts`) so the UI isn't empty on cold start.

---

## 5.7 Process snapshots (the full-view table)

When the user clicks a card's chart area, we open the **full view** window (frontend §11) — maximized chart on top, a 20-row process table below. For the table to show "what was running at *this* timestamp," every probe tick needs a snapshot of the top processes by each metric.

### 5.7.1 What to collect — top-20 per metric, ranked on the probe

Shipping every process every 10s is wasteful. The probe ranks server-side and sends only the top 20 for each metric.

- **CPU**: `ps -eo pid,comm,%cpu,rss,nlwp,lstart,user --sort=-%cpu | head -21`
- **Memory**: `--sort=-rss`, same columns
- **Network**: `nethogs -t -c 1 -d 1` if installed — emits `<program>/<pid>/<uid>  sent  recv` for one second. Sum `sent + recv` per PID, rank desc, keep top 20. If `nethogs` is missing, probe emits `procs_net_available: false` and the Mac-side table shows an install hint for that metric.
- **Disk I/O**: `/proc/<pid>/io` on Linux — world-readable (no root), exposes `read_bytes` / `write_bytes` (actual storage-layer bytes) and `rchar` / `wchar` (VFS-layer; includes page cache hits). Two samples 1s apart → per-process `read_bps` / `write_bps`. Rank by `read_bps + write_bps` desc, keep top 20. Darwin servers have no shell-level equivalent (`proc_pid_rusage` is a C API; `fs_usage` needs root) — emit `procs_disk_available: false` and surface the install-hint empty state, same pattern as network.

  **Caveat worth knowing:** `/proc/<pid>/io`'s `read_bytes`/`write_bytes` are what the block layer saw, so they exclude cache hits but include writeback from earlier writes. Sustained values still correlate with real disk pressure, which is what the user cares about ("who's thrashing my disk?"). We use `read_bytes`/`write_bytes` (not `rchar`/`wchar`) to avoid double-counting cached reads.

  Payload cost: ~2KB per tick for disk (top-20 rows × ~80B), scanning `/proc/*/io` costs ~5–10ms on a busy box. Tolerable at 10s cadence.

Wire format extends the probe output with four arrays:

```
procs_cpu=[{"pid":18421,"comm":"postgres: autovacuum worker","user":"postgres","cpu":47.2,"rss":1932735283,"threads":4,"started":"13:14:02"}, ...]
procs_mem=[...]
procs_net=[...]    # empty array when nethogs absent; procs_net_available=false
procs_disk=[{"pid":18421,"comm":"postgres: autovacuum worker","user":"postgres","read_bps":0,"write_bps":81788928,"threads":4,"started":"13:14:02"}, ...]
```

Per-tick payload grows by ~4 × 20 × ~80B ≈ **~6–7KB**, well within SSH budget.

**macOS servers:** `ps -Ao pid,comm,%cpu,rss,user -r` (CPU-sorted), `-m` (memory). No native per-process network or disk-I/O story without sudo/eBPF — probe emits `procs_net: []` and `procs_disk: []` with `procs_net_available: false` / `procs_disk_available: false`, and the same install-hint path applies.

### 5.7.2 Storage — `process_sample` table

```sql
CREATE TABLE process_sample (
    host_id  TEXT NOT NULL,
    ts       INTEGER NOT NULL,
    metric   TEXT NOT NULL,        -- 'cpu' | 'mem' | 'net' | 'disk'
    rank     INTEGER NOT NULL,     -- 1..20
    pid      INTEGER,
    command  TEXT,
    user     TEXT,
    value    REAL,                 -- primary ranking value (metric-dependent; see below)
    value_a  REAL,                 -- secondary metric-specific value (metric-dependent)
    value_b  REAL,                 -- tertiary metric-specific value
    threads  INTEGER,
    started  TEXT,                 -- short timestamp string from ps lstart
    PRIMARY KEY (host_id, ts, metric, rank),
    FOREIGN KEY (host_id, ts) REFERENCES sample(host_id, ts) ON DELETE CASCADE
) WITHOUT ROWID;
```

Column meaning varies by `metric`:

| metric | `value` (ranked) | `value_a` | `value_b` |
|---|---|---|---|
| `cpu`  | `cpu_pct` (0..100)   | `rss` bytes        | — |
| `mem`  | `rss` bytes          | `cpu_pct`          | — |
| `net`  | `rx_bps + tx_bps`    | `rx_bps`           | `tx_bps` |
| `disk` | `read_bps + write_bps` | `read_bps`       | `write_bps` |

Generic `value_a` / `value_b` columns instead of named `rx_bps`/`write_bps` etc. avoids sparse-column bloat on a `WITHOUT ROWID` table. The UI knows which metric it's rendering and picks the right label.

**Sizing.** 20 hosts × 8,640 ticks/day × 4 metrics × 20 rows × ~80B = **~107 MB/day**. Still comfortable at 24h retention, and cascade delete when `sample` rows are purged keeps it bounded.

If this ever becomes a problem, two cheap levers: drop to one snapshot every 30s (every 3rd probe) — process lists change slowly — or cap to top 10. Don't optimize prematurely.

### 5.7.3 Read path — SQLite on hover, no ring buffer

The per-server in-memory ring (§5.6) does **not** include process rows. Holding 3 × 20 × 8,640 process rows per host in RAM is ~40MB × 20 hosts ≈ **800MB** — a non-starter. Query SQLite instead; the hover query is a single composite-PK hit:

```sql
SELECT rank, pid, command, user, value, value_a, value_b, threads, started
  FROM process_sample
 WHERE host_id = ? AND ts = ? AND metric = ?
 ORDER BY rank;
```

<1ms with the `WITHOUT ROWID` PK. The full view debounces hover by ~100ms so rapid mouse drag doesn't hammer the DB; a paused (pinned) state issues one query and caches.

### 5.7.4 Cold-start and retention

- On window open, run the query for the latest `ts` immediately — the table doesn't stay empty waiting for a hover.
- `process_sample` honors the same 24h retention (§5). No separate purge job; the FK cascade from `sample` takes care of it.
- If the user freezes (pins) at a timestamp older than 24h — can't happen, the chart itself doesn't have that data. Not a concern.

---

## 6. Settings & configuration file

User-tweakable behavior (thresholds, disk exclusions, notification prefs, cadence overrides) lives in a single JSON file at `~/Library/Application Support/Towertail/settings.json`. Not YAML — we have `JSONEncoder`/`JSONDecoder` for free, Xcode diffs it well, and the schema is small.

Why JSON over `UserDefaults` alone: per-host overrides and nested structures (disk exclusions per host, quiet hours, tag filters) are painful as flat `@AppStorage` keys. Scalar prefs (launch-at-login, appearance) can still use `UserDefaults`; structured prefs go in the file.

**Atomic writes.** Write to `settings.json.tmp` then `rename(2)` — never let a crash mid-write corrupt user config. Reload on `DispatchSource.makeFileSystemObjectSource(.write)` so hand-edits (advanced users) take effect without restart.

### 6.1 Shape

```json
{
  "version": 1,
  "general": {
    "launch_at_login": true,
    "appearance": "auto",
    "poll_interval_seconds": 10
  },
  "thresholds": {
    "default": {
      "cpu":     { "warn": 0.75, "critical": 0.90 },
      "memory":  { "warn": 0.80, "critical": 0.92 },
      "disk":    { "warn": 0.85, "critical": 0.95 },
      "network": { "warn_mbps": 500, "critical_mbps": 900 }
    },
    "per_host": {
      "<host_id>": {
        "cpu":  { "warn": 0.60, "critical": 0.80 },
        "disk": { "warn": 0.90, "critical": 0.97 }
      }
    }
  },
  "disks": {
    "excluded_mounts": {
      "<host_id>": ["/mnt/backups", "/data/scratch"]
    }
  },
  "network": {
    "excluded_interfaces": {
      "<host_id>": ["docker0", "br-a1b2c3"]
    },
    "included_virtual_interfaces": {
      "<host_id>": ["tailscale0"]
    }
  },
  "notifications": {
    "warn_enabled": true,
    "critical_enabled": true,
    "renotify_minutes": 30,
    "quiet_hours": { "start": "22:00", "end": "07:00" }
  },
  "hosts": {
    "<host_id>": {
      "display_name": "db-primary",
      "connection_string": "ubuntu@db-primary.tail-scale.ts.net",
      "tags": ["prod", "db"]
    }
  }
}
```

### 6.2 Disk exclusions — the user-facing knob

The disks pane under Preferences (`res: research-frontend.md §9`) lists detected mounts per host with a checkbox column. Unchecking writes the mount string into `disks.excluded_mounts[<host_id>]`. On write:

1. The settings file is updated atomically.
2. `ServerStore` reloads; the affected host's ring buffer drops the excluded mount from the compact-card aggregation *but retains its ring* (the user may re-enable it).
3. The SQLite `disk_sample` rows for that mount are **kept** — excluding is a view filter, not a retention policy. Toggling back on instantly restores history.
4. Any firing alert for the excluded mount is cleared; no new alerts are evaluated against it.

`excluded_mounts` is keyed by `host_id` (machine-id) not hostname, so renaming a host doesn't silently re-include excluded mounts.

### 6.2.5 Network exclusions / inclusions

Two knobs, because network filtering is two-directional:

- **`network.excluded_interfaces[<host_id>]`** — interfaces that *would* be aggregated by default (real NICs, plus `tailscale0`/`wg*`) but the user wants out. Same toggle semantics as disk: collected, not aggregated, no history loss.
- **`network.included_virtual_interfaces[<host_id>]`** — the reverse: interfaces the probe classifies as virtual-and-uninteresting that the user wants back in the aggregate. Mostly `utun*` on Darwin servers running Tailscale, or a user who genuinely wants to monitor Docker bridge traffic (rare, but we shouldn't block it).

Precedence when both are set: **exclude wins**. If an interface name appears in both lists the user is confused; we drop it from the aggregate and surface a validation warning in the Network pane. Default lists are empty; the built-in rules in §1.7 do the right thing for >95% of hosts.

### 6.3 Secrets do *not* go in this file

SSH passphrases, API tokens, anything sensitive lives in Keychain (`kSecClassGenericPassword`, per-host account). `settings.json` is plaintext and may be synced via Dotfiles/Time Machine — treat it as public config, not secrets.

### 6.4 Migration

Start at `"version": 1`. On load, if the file's version is older than the current schema, run sequential migrations in a `Settings.migrate(from:to:)` helper before decoding. Unknown keys are preserved round-trip (decode into a residual `[String: Any]` bag and re-encode) so a downgrade doesn't silently strip config written by a newer build.

---

## 7. Native notifications

```swift
import UserNotifications
let center = UNUserNotificationCenter.current()
try await center.requestAuthorization(options: [.alert, .sound, .badge])
```

Ask on first enable of a notification preference. Check `center.notificationSettings().authorizationStatus` before each dispatch. Set `UNNotificationContent.interruptionLevel = .active` (or `.timeSensitive` for critical — requires entitlement). No special `Info.plist` keys needed for a menu-bar app.

**Threshold evaluation against SQLite** (not an in-memory accumulator — survives restart). For "CPU > 90% for 5 min":

```sql
SELECT MIN(cpu_pct) AS low FROM sample
 WHERE host_id = ? AND ts >= strftime('%s','now') - 300;
```

If `low > 90`, rule is firing. Combine with `alert_state`:

```swift
enum AlertPhase { case clear, firing(since: Date), snoozed(until: Date) }

switch (firing, state.phase) {
case (true,  .clear):
    state.phase = .firing(since: .now); notify(.raised)
case (true,  .firing) where state.lastNotified < .now - 30.minutes:
    notify(.still)                      // re-notify every 30m max
case (false, .firing):
    state.phase = .clear; notify(.resolved)
default: break
}
```

Key ideas:

1. **One notification per state edge**, plus a long re-reminder interval.
2. **Persisted state** in `alert_state` — no double-notify across app restart.
3. **`UNNotificationRequest.identifier = "\(hostId).\(mount ?? "-").\(rule.id)"`** so `add` replaces an existing notification for free, including per-mount disk alerts.
4. **Snooze**: notification action button writes `snoozed(until:)`; skip evaluation while snoozed.

**Disk alert evaluation** differs from CPU/mem because it's per-mount. For each host, iterate its *included* mounts (per §6.2) and evaluate the rule independently:

```sql
SELECT mount, MAX(used * 1.0 / total) AS pct
  FROM disk_sample
 WHERE host_id = ? AND ts >= strftime('%s','now') - 300
 GROUP BY mount;
```

Any row with `pct > 0.90` fires `disk_gt_90` scoped to `(host_id, mount)`. Notification text reads `/data on db-primary at 92%`, not `db-primary disk at 92%`.

---

## Summary

- **Collection**: one `probe.sh` per host, dispatched via `/usr/bin/ssh` with `ControlMaster`. OS-branch inside the script; emit one line of `key=value` + a JSON disks array.
- **SSH**: shell out to system SSH — Tailscale MagicDNS + ssh-sampler + `known_hosts` all work for free. Not sandbox-compatible; Developer ID distribution.
- **Tailscale**: `tailscale status --json`, filter by `Online` + tag; refresh every 30s.
- **Identity**: `machine_id` from `/etc/machine-id` (Linux) or `IOPlatformUUID` (Darwin), falling back to a Mac-generated UUID persisted at `~/.towertail/host-id`. Hostname is mutable display metadata.
- **Storage**: SQLite/GRDB, `(host_id, ts)` composite PK, ~20MB for 20 hosts × 24h × 10s. WAL + 10-minute purge job.
- **Cadence**: 10s, jittered ±2s, per-host actor, persistent SSH via `ControlMaster`, exponential backoff on failure.
- **In-memory**: full 24h ring buffer (`ContiguousArray<Sample>`, ~16MB for CPU/mem/net + ~20MB for per-mount disk) on each `ServerViewModel`; hover tooltip reads directly with no SQLite roundtrip.
- **Disks**: collect every real mount; compact card shows `max(used/total)` across included mounts (averaging hides outages); maximized view has a per-mount dropdown; alerts are per-mount.
- **Network**: collect every interface; real NICs (non-virtual via `/sys`) + `tailscale0`/`wg*` aggregate by default; `docker*`/`veth*`/`br-*`/`lo` always excluded; compact card and alerts use the **sum** across included interfaces; maximized view defaults to "All" with a per-interface drill-down; `net_sample` is per-interface (aggregate computed at read time).
- **Processes**: probe ranks server-side and sends top-20 for each of CPU/mem/net/disk per tick; Linux uses `/proc/<pid>/io` for disk I/O (no root needed); Darwin shows an install-hint for net and disk; `process_sample` table (~107MB/day at 20 hosts × 24h); full-view hover reads SQLite directly (no ring, ~800MB memory cost avoided).
- **Settings**: JSON at `~/Library/Application Support/Towertail/settings.json` for structured prefs (per-host thresholds, excluded mounts/interfaces, notifications, quiet hours); atomic writes; secrets stay in Keychain.
- **Notifications**: `UNUserNotificationCenter`, persisted `alert_state` keyed by `(host_id, mount, rule)`, edge-triggered with long re-notify interval and snooze.
