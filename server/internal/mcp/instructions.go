package mcp

// instructions is the text of the initialize result. It gives facts for the
// model and gives no orders. Keep it at 2 KB or less.
const instructions = `Towertail monitors a fleet of servers (hosts). These tools read the fleet of one organization.

Hosts: a tool takes a host by ID or by display name (no case). A name that matches two hosts gives an error with both IDs. An empty host means the whole fleet where the tool allows it.

Ranges: "range" is a duration from 15m to 365d, for example 90m, 24h or 7d. "from" and "to" are RFC 3339 times and replace "range". A "to" in the future moves to now.

Units: CPU is a percent (0 to 100). Memory and disk use are a ratio (0 to 1). Network and disk throughput are bytes each second. Sizes are bytes. Times are RFC 3339 in UTC.

Plan limit: the plan of the organization sets how far back history goes. When the limit moves the start of a range later, the result has "clamped": true.

Offline hosts have no current values. A count of 0 means no data in the range.

Write tools, where they exist, need a scope that the sign-in granted and a role of the user that allows the change. A tool that the caller cannot use gives a tool error when called.`
