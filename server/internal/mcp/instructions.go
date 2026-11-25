package mcp

// instructions is the text of the initialize result. It gives facts for the
// model and gives no orders. Keep it at 2 KB or less.
const instructions = `Towertail monitors a fleet of servers (hosts). These tools read the fleet of one organization.

Hosts: a tool takes a host by ID or by display name (no case). A name that matches two hosts gives an error with both IDs. An empty host means the whole fleet where the tool allows it.

Ranges: "range" is a duration from 15m to 365d, for example 90m, 24h or 7d. "from" and "to" are RFC 3339 times and replace "range". A "to" in the future moves to now.

Units: CPU is a percent (0 to 100). Memory and disk use are a ratio (0 to 1). Network and disk throughput are bytes each second. Sizes are bytes. Times are RFC 3339 in UTC.

Plan limit: the plan of the organization sets how far back history goes. When the limit moves the start of a range later, the result has "clamped": true.

Offline hosts have no current values. A count of 0 means no data in the range.

Process cpu uses the top convention: 100 is one full core. Rule limits use the units of the rules: call reference with topic "thresholds".
whoami gives the plan and the history limit.
For the meaning of a metric, a rule or a plan limit, call reference.
To find why alerts came, use alert_stats, then get_alert and host_rules.
To find why a metric changed, use explain_change.
To test new limits, use suggest_thresholds. It replays stored data and gives the number of alerts that each limit would make.
To find how long a process runs, use top_processes or process_history. started_at is exact. first_seen is the oldest stored time.

Write tools change alerts or rules. They need a write scope, a role and the org setting Changes by AI. A tool that the caller cannot use gives a tool error. set_thresholds, ignore_process and an org mute_metric save a rules backup. The result gives change.backup_id. undo_rules_change restores it.`
