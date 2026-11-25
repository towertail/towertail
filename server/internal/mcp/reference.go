package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
)

// Topics of the reference tool.
var referenceTopics = []string{"metrics", "thresholds", "alerts", "long_process", "history", "plan"}

// ReferenceIn is the input of reference.
type ReferenceIn struct {
	Topic string `json:"topic" jsonschema:"one of: metrics, thresholds, alerts, long_process, history, plan"`
}

// ReferenceFact is one fact. Value and Unit are set when the fact is a
// number.
type ReferenceFact struct {
	Name  string   `json:"name"`
	Value *float64 `json:"value,omitempty"`
	Unit  string   `json:"unit,omitempty"`
	Text  string   `json:"text"`
}

// ReferenceOut is the output of reference.
type ReferenceOut struct {
	Topic string          `json:"topic"`
	Facts []ReferenceFact `json:"facts"`
}

func (s *Server) reference(ctx context.Context, _ *sdk.CallToolRequest, in ReferenceIn) (*sdk.CallToolResult, ReferenceOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, ReferenceOut{}, err
	}
	facts, err := s.orgFacts(ctx, c.Token.OrgID)
	if err != nil {
		return nil, ReferenceOut{}, s.toolError("reference", err)
	}
	out, err := s.ref.Topic(in.Topic, facts)
	if err != nil {
		return nil, ReferenceOut{}, err
	}
	return nil, out, nil
}

// Reference gives the facts behind the metrics, the rules and the plan.
// The facts come from the defaults and the limits in the code, so they
// stay true. The text states facts and gives no orders.
type Reference struct {
	defaults store.ServerSettings
}

func NewReference() Reference { return Reference{defaults: store.DefaultServerSettings()} }

// Topic gives the facts of a topic. An unknown topic is an input error.
func (r Reference) Topic(name string, org OrgFacts) (ReferenceOut, error) {
	var facts []ReferenceFact
	switch name {
	case "metrics":
		facts = r.metrics()
	case "thresholds":
		facts = r.thresholds()
	case "alerts":
		facts = r.alerts()
	case "long_process":
		facts = r.longProcess()
	case "history":
		facts = r.history()
	case "plan":
		facts = r.plan(org)
	default:
		return ReferenceOut{}, badf("topic must be one of %s", strings.Join(referenceTopics, ", "))
	}
	return ReferenceOut{Topic: name, Facts: facts}, nil
}

func num(name string, v float64, unit, text string) ReferenceFact {
	return ReferenceFact{Name: name, Value: &v, Unit: unit, Text: text}
}

func note(name, text string) ReferenceFact { return ReferenceFact{Name: name, Text: text} }

// metricFacts gives the unit and the meaning of each history metric.
var metricFacts = map[string]struct{ unit, text string }{
	"cpu":          {"percent", "CPU use of the whole host, 0 to 100."},
	"mem":          {"ratio 0 to 1", "Used memory divided by total memory."},
	"net_rx":       {"bytes each second", "Bytes received on all interfaces."},
	"net_tx":       {"bytes each second", "Bytes sent on all interfaces."},
	"disk_read":    {"bytes each second", "Bytes read from all disks."},
	"disk_write":   {"bytes each second", "Bytes written to all disks."},
	"load":         {"load average", "The same as load_1."},
	"load_1":       {"load average", "The load average of the last minute."},
	"procs":        {"count", "The number of processes."},
	"disk":         {"ratio 0 to 1", "The use of the fullest mount."},
	"inodes":       {"ratio 0 to 1", "Inode use of the mount with the highest inode use."},
	"zombies":      {"count", "The number of zombie processes."},
	"pids":         {"ratio 0 to 1", "Used process IDs divided by the limit."},
	"files":        {"ratio 0 to 1", "Open files divided by the limit."},
	"psi_cpu":      {"percent", "Linux pressure stall time of CPU (some)."},
	"psi_mem":      {"percent", "Linux pressure stall time of memory (some). The rule metric memPressure compares its warn limit with it."},
	"psi_mem_full": {"percent", "Linux pressure stall time of memory (full). The rule metric memPressure compares its critical limit with it."},
	"psi_io":       {"percent", "Linux pressure stall time of I/O (some)."},
	"psi_io_full":  {"percent", "Linux pressure stall time of I/O (full). The rule metric ioPressure compares both limits with it."},
	"mem_pressure": {"percent", "Memory pressure of a Mac, 0 to 100. The rule metric memPressure uses the Mac pair (macMemPressure) for it."},
}

func (Reference) metrics() []ReferenceFact {
	var out []ReferenceFact
	for _, m := range clickhouse.MetricNames() {
		f := metricFacts[m]
		out = append(out, ReferenceFact{Name: m, Unit: f.unit, Text: f.text})
	}
	return out
}

// thresholdUnit is the unit of the warn and critical level of a rule metric.
func thresholdUnit(m store.ThresholdMetric) string {
	switch {
	case m == store.MetricMemPressure || m == store.MetricIOPressure:
		return "percent"
	case m.Fraction():
		return "ratio 0 to 1"
	}
	return "count"
}

func (r Reference) thresholds() []ReferenceFact {
	d, rules := r.defaults, r.defaults.Alerts
	out := []ReferenceFact{
		note("units", `The limits of cpu, mem and disk are a ratio from 0 to 1: 0.75 is 75 percent. The history metric cpu is a percent.`),
		note("warn", "warn is the level that opens a warn alert."),
		note("critical", "critical is the level that opens a critical alert."),
	}
	for _, m := range slices.Concat(store.UsageMetrics, store.HealthMetrics) {
		p := d.Thresholds.Pair(m)
		out = append(out,
			num(string(m)+".warn", p.Warn, thresholdUnit(m), "Default warn limit of "+string(m)+"."),
			num(string(m)+".critical", p.Critical, thresholdUnit(m), "Default critical limit of "+string(m)+"."),
		)
	}
	for _, m := range []string{"cpu", "mem", "disk", "health"} {
		rule, _ := rules.For(m)
		out = append(out,
			num(m+".sustain", float64(rule.SustainSeconds), "seconds", "Default sustain time of "+m+": the time that the level must hold before the alert opens."),
			note(m+".notify", fmt.Sprintf("Default notify level of %s is %q.", m, rule.Notify)),
		)
	}
	return append(out,
		num("sustain.max", store.MaxSustainSeconds, "seconds", "The longest sustain time of a rule."),
		num("tolerance", rules.Tolerance, "ratio", "tolerance is the part of the sustain window that must be over the limit."),
		num("tolerance.min", store.MinTolerance, "ratio", "The lowest tolerance."),
		num("tolerance.max", store.MaxTolerance, "ratio", "The highest tolerance."),
		num("cooldown", float64(d.NotifyCooldownSeconds), "seconds", "cooldown: the minimum time between two notifications with the same host, metric and tint."),
		num("debounce", float64(d.NotifyDebounceSeconds), "seconds", "debounce: the time that a new level must hold before the alert changes level."),
		note("notify.levels", `notify is "off" (none), "critical" (critical only) or "all" (warn and critical).`),
		note("memPressure", "On Linux, memPressure compares warn with psi_mem and critical with psi_mem_full. On a Mac it uses the pair macMemPressure."),
		note("ioPressure", "ioPressure compares both limits with psi_io_full."),
		note("sources", `A host rule replaces the org rule of the same metric. The source of an effective rule is "org" or "host".`),
	)
}

func (Reference) alerts() []ReferenceFact {
	return []ReferenceFact{
		note("alert", "An alert opens when a metric leaves ok and closes when it returns to ok."),
		note("states", "open: not resolved. acked: a user acknowledged it. snoozed: notifications are paused until a time. resolved: the metric returned to ok."),
		note("action", "The filter state action means open, not acked and not snoozed."),
		note("tints", "warn and critical. The severity of an alert is its worst tint."),
		note("kinds", "cpu, mem and disk are usage alerts. health groups the health checks. long is a long-running process. nodata means the host stopped reporting."),
		num("range.max", query.MaxAlertDays, "days", "The longest range of the alert list."),
		num("retention", 365, "days", "The server keeps alert events and the alert log for 365 days. The plan can shorten the history that a call reads."),
		note("flapping", "A host and metric flaps when it has 3 or more alerts in the range and the median alert length is under 15 minutes."),
	}
}

func (r Reference) longProcess() []ReferenceFact {
	d := r.defaults.LongProc
	lo, hi := store.LongProcRule{}.Clamped(), store.LongProcRule{CPUPct: 1e9, SustainSeconds: 1 << 30}.Clamped()
	return []ReferenceFact{
		note("rule", "The long-process rule flags a process that stays over a CPU limit for a long time."),
		note("enabled", fmt.Sprintf("The rule is enabled by default: %t.", d.Enabled)),
		num("cpu", d.CPUPct, "percent of one core", "Default CPU limit. 100 is one full core (the top convention)."),
		num("cpu.min", lo.CPUPct, "percent of one core", "The lowest CPU limit."),
		num("cpu.max", hi.CPUPct, "percent of one core", "The highest CPU limit."),
		num("sustain", float64(d.SustainSeconds), "seconds", "Default time over the limit."),
		num("sustain.min", float64(lo.SustainSeconds), "seconds", "The shortest time."),
		num("sustain.max", float64(hi.SustainSeconds), "seconds", "The longest time."),
		num("tolerance", store.LongProcTolerance, "ratio", "The part of the time that the process must be at or over the limit."),
		note("notify", `notify is "off" or "all". Default is "all".`),
		note("ignore", "A process name in an ignore list never alerts. Names match with no case. The org list and the host lists add up."),
		note("process", "A process is a name, a command and a repo. Its key is the same across samples and rollups."),
	}
}

func (Reference) history() []ReferenceFact {
	return []ReferenceFact{
		note("tiers", "A range reads one table. 6 hours or less reads raw samples. 7 days or less reads the 1 minute rollup. 30 days or less reads the 5 minute rollup. A longer range reads the 1 hour rollup."),
		note("age", "The age of the start also picks the table: raw samples are for the last 7 days, the 1 minute rollup for 30 days, the 5 minute rollup for 90 days."),
		num("raw.retention", 7, "days", "Raw samples."),
		num("1m.retention", 30, "days", "The 1 minute rollup."),
		num("5m.retention", 90, "days", "The 5 minute rollup."),
		num("1h.retention", 365, "days", "The 1 hour rollup."),
		num("processes.raw.retention", 1, "hours", "Process rows with the start time and the PID."),
		num("processes.1m.retention", 7, "days", "The top 10 processes of each minute."),
		num("processes.1h.retention", 365, "days", "The top 5 processes of each hour."),
		note("clamped", "The plan sets how far back a call can read. A result has clamped true when the limit moved the start later."),
		note("no data", "A metric that a host does not report, and a bucket with no value, have no points. count 0 means no data."),
	}
}

func (Reference) plan(org OrgFacts) []ReferenceFact {
	out := []ReferenceFact{
		note("plan", fmt.Sprintf("The plan of the organization is %q.", org.Plan)),
		num("history_days", float64(org.HistoryDays), "days", "How far back history goes. 0 means no limit."),
	}
	if org.MaxHosts > 0 {
		out = append(out, num("max_hosts", float64(org.MaxHosts), "hosts", "The most hosts that the plan allows."))
	}
	return out
}
