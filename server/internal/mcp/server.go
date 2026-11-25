// Package mcp serves the read-only Model Context Protocol tools of the
// server. The tools read through the query service, so they follow the
// same plan limits as the REST API.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/internal/scan"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// FleetQueries are the host reads.
type FleetQueries interface {
	HistoryDays(ctx context.Context, orgID uuid.UUID) int
	Hosts(ctx context.Context, orgID uuid.UUID, sort query.HostSort, limit int) (wire.FleetHosts, error)
	HostNames(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]string, error)
	ResolveHost(ctx context.Context, orgID uuid.UUID, ref string) (store.Node, error)
	HostsByTag(ctx context.Context, orgID uuid.UUID, tag string) ([]store.Node, error)
	HostDetail(ctx context.Context, orgID, nodeID uuid.UUID) (query.HostDetail, error)
	FleetSummary(ctx context.Context, orgID uuid.UUID) (query.FleetSummary, error)
}

// HistoryQueries are the metric and disk reads.
type HistoryQueries interface {
	HistoryMulti(ctx context.Context, r query.HistoryMultiRequest) (query.HistoryMultiResult, error)
	Disks(ctx context.Context, orgID, nodeID uuid.UUID, from, to time.Time) (query.DiskResult, error)
}

// ProcessQueries are the process reads.
type ProcessQueries interface {
	TopProcesses(ctx context.Context, q query.ProcQuery) (wire.FleetProcesses, error)
	ProcessesAt(ctx context.Context, r query.SnapshotRequest) (wire.ProcessSnapshot, error)
	ProcessStarts(ctx context.Context, orgID uuid.UUID, keys []clickhouse.ProcStartKey, at time.Time) (map[clickhouse.ProcStartKey]query.ProcStart, error)
	ResolveProcess(ctx context.Context, orgID, nodeID uuid.UUID, ref string, from, to time.Time) (wire.ProcessKey, error)
	ProcessHistory(ctx context.Context, r query.ProcessHistoryRequest) (query.ProcessHistory, error)
}

// AlertQueries are the alert reads.
type AlertQueries interface {
	ListAlerts(ctx context.Context, q query.AlertQuery) (wire.AlertPage, error)
	AlertDetail(ctx context.Context, orgID, id uuid.UUID) (wire.AlertDetail, error)
	Rules(ctx context.Context, orgID, nodeID uuid.UUID) (query.RulesResult, error)
}

// AnalysisQueries are the alert and metric analyses.
type AnalysisQueries interface {
	AlertStats(ctx context.Context, r query.AlertStatsRequest) (query.AlertStats, error)
	ExplainChange(ctx context.Context, r query.ChangeRequest) (query.ChangeReport, error)
	SuggestThresholds(ctx context.Context, r query.SuggestRequest) (query.Suggestion, error)
}

// Queries is the part of the query service that the tools read. A test
// fake can embed one group and stub the rest.
type Queries interface {
	FleetQueries
	HistoryQueries
	ProcessQueries
	AlertQueries
	AnalysisQueries
}

// Scans reads the disk scans of a node.
type Scans interface {
	List(ctx context.Context, orgID, nodeID uuid.UUID) ([]scan.Job, error)
	Get(ctx context.Context, id uuid.UUID) (scan.Job, error)
	Result(ctx context.Context, id uuid.UUID) ([]byte, error)
}

// OrgFacts are the facts of an org that the queries do not hold. Plan is
// the plan name. MaxHosts is 0 when the plan has no host limit.
type OrgFacts struct {
	Name, Plan  string
	HistoryDays int
	MaxHosts    int
}

// OrgInfo gives the org facts. The cloud sets it. A nil OrgInfo gives the
// self-hosted facts.
type OrgInfo interface {
	OrgFacts(ctx context.Context, orgID uuid.UUID) (OrgFacts, error)
}

// ToolSet names one set of tools. Each set has its own MCP server and its
// own HTTP path.
type ToolSet string

const (
	// ToolSetLatest is served at /mcp. It follows the newest tools.
	ToolSetLatest ToolSet = "latest"
	// ToolSetV1 is served at /mcp/v1. Its tool schemas stay frozen, because
	// directory reviews depend on them.
	ToolSetV1 ToolSet = "v1"
)

var toolSets = []ToolSet{ToolSetLatest, ToolSetV1}

// SettingsReader gives the settings of an org.
type SettingsReader interface {
	GetSettings(ctx context.Context, orgID uuid.UUID) (store.ServerSettings, error)
}

// Options holds the dependencies of the server.
type Options struct {
	Version  string
	Log      *slog.Logger
	Settings SettingsReader
	// Scans is nil when disk scans are off.
	Scans Scans
	// OrgInfo is nil on a self-hosted server.
	OrgInfo OrgInfo
	// AppURL is the base URL of the dashboard. Empty means no url in
	// search and fetch.
	AppURL string
	// Actions runs the write tools. With nil, the tools are listed, and a
	// call gives an error.
	Actions Actions
}

// setServer is the MCP server of one tool set.
type setServer struct {
	srv   *sdk.Server
	order []string
}

// Server holds the MCP servers of the tool sets and their tools.
type Server struct {
	q        Queries
	scans    Scans
	orgInfo  OrgInfo
	settings SettingsReader
	actions  Actions
	appURL   string
	version  string
	ref      Reference
	sets     map[ToolSet]*setServer
	tools    map[string]*toolEntry
	names    []string // sorted
	log      *slog.Logger
	now      func() time.Time
}

// New makes the server with its tools.
func New(q Queries, o Options) *Server {
	s := &Server{
		q:        q,
		scans:    o.Scans,
		orgInfo:  o.OrgInfo,
		settings: o.Settings,
		actions:  o.Actions,
		appURL:   o.AppURL,
		version:  o.Version,
		ref:      NewReference(),
		sets:     make(map[ToolSet]*setServer, len(toolSets)),
		tools:    make(map[string]*toolEntry),
		log:      o.Log,
		now:      time.Now,
	}
	for _, set := range toolSets {
		s.sets[set] = &setServer{srv: sdk.NewServer(
			&sdk.Implementation{Name: "towertail", Version: o.Version},
			&sdk.ServerOptions{Instructions: instructions},
		)}
	}
	// A handler returns a nil result, or leaves Content nil. Then the SDK
	// adds the JSON text block of the output. Google reads only that block.
	addTool(s, readTool("fleet_summary", "Fleet summary", "Get the state of the fleet at a glance: host counts, open alerts, hosts over a limit, the top 5 hosts by CPU, memory and disk, and the alerts and notifications of the last 24 hours."), s.fleetSummary)
	addTool(s, readTool("list_hosts", "List hosts", "List the hosts of the fleet with the latest CPU (percent), memory and disk (ratio 0 to 1), load and network (bytes each second). Offline hosts have no values."), s.listHosts)
	addTool(s, readTool("get_host", "Get host", "Get one host: its latest values, facts (OS, kernel, cores, memory, uptime), open alerts and the effective limits with their source (org or host)."), s.getHost)
	addTool(s, readTool("host_metrics", "Host metrics", "Get the history of metrics of one host, of many hosts or of a tag: min, avg, max, last and a capped list of points for each metric. Any history metric works (see reference). With compare, each metric also has the window before the range and the change of the average. The plan can shorten the range (clamped)."), s.hostMetrics)
	addTool(s, readTool("top_processes", "Top processes", "Get the top processes of one host, or of the fleet, by CPU, memory or disk I/O over a range. With at and a host, get the processes at one time, with PID, user and state. Each process has its start time and run time where known."), s.topProcesses)
	addTool(s, readTool("process_history", "Process history", "Get the history of one process of a host: CPU, memory and disk I/O points, the stats of the range, the facts (PID, user, ports) and the start time. The process is a key from top_processes or a name."), s.processHistory)
	addTool(s, readTool("alerts", "List alerts", "List the alerts of the fleet or of one host: the open alerts by default, or the alerts of a range. Filter by state, kind, severity and text. Each alert has its severity, state, value, peak, start and end. The result also has the counts of each state."), s.alerts)
	addTool(s, readTool("get_alert", "Get alert", "Get one alert: its timeline (opened, changed, notified, acked, snoozed, recovered), the limits that it had, the rule that applies to its host now and the number of notifications."), s.getAlert)
	addTool(s, readTool("host_rules", "Host rules", "Get the alert rules of the organization, or of one host: limits, sustain times, notify levels, muted metrics, the long-process rule and the notification switches. For a host, also the overrides and the effective rules with the source (org or host) of each."), s.hostRules)
	addTool(s, readTool("disks", "Disk use", "Get the use of each disk mount of one host and the change over a range."), s.disks)
	addTool(s, readTool("disk_scans", "Disk scans", "Get the disk scans of one host: the newest 5 jobs and, for the newest finished scan or a job_id, the file system, the 20 largest entries and the cleanup recommendations."), s.diskScans)
	addTool(s, readTool("alert_stats", "Alert statistics", "Get the statistics of the alerts of a range: the totals, the notifications (and the ones sent by email), the alerts and notifications by host, kind, rule and channel, the metrics that flap (many short alerts) and the alert starts over time. Use it first to find why alerts came."), s.alertStats)
	addTool(s, readTool("explain_change", "Explain a metric change", "Find the level shifts of one metric of one host over a range, such as a jump of CPU use. For the largest shift, get the processes that rose or fell (CPU, memory or disk I/O, by metric) and the alerts of the host that started around it. Use it to find why a metric changed."), s.explainChange)
	addTool(s, readTool("suggest_thresholds", "Test alert limits", "Replay the stored cpu, mem or disk values of one host, or of the hosts on the organization limits, under the current limits and other pairs of limits. Each pair gives the number of alerts, critical alerts and notifications that it would have made, with the time spent over each limit. Compare with the alerts that really happened. The data is the 1 minute rollup, so the counts are approximate."), s.suggestThresholds)
	addTool(s, readTool("reference", "Reference facts", "Get the facts behind a metric, a rule or a plan limit. Topics: metrics, thresholds, alerts, long_process, history and plan."), s.reference)
	addTool(s, readTool("search", "Search", "Search the organization by text: host names and tags, the alerts of the last 7 days and the reference topics. Each result has an id for fetch."), s.search)
	addTool(s, readTool("fetch", "Fetch", "Get the full document of a search result: a host (as get_host), an alert (as get_alert) or a reference topic (as reference). The text is JSON."), s.fetch)
	addTool(s, readTool("whoami", "Who am I", "Get the organization, the plan, the history limit, the sign-in (token kind, role and scopes) and the changes that this connection can make."), s.whoami)
	addTool(s, writeTool("ack_alert", "Acknowledge alert", "Acknowledge an open alert. An acknowledged alert sends no more notifications. With undo, take the acknowledgement back. Needs the scope alerts:write and the role member. The result has the alert and change.", true), s.ackAlert)
	addTool(s, writeTool("snooze_alert", "Snooze alert", "Snooze an open alert until a time (until, RFC 3339) or for a time (for, 5m to 30d). A snoozed alert sends no notifications. With undo, remove the snooze. Needs the scope alerts:write and the role member. The result has the alert and change.", true), s.snoozeAlert)
	addTool(s, writeTool("snooze_host", "Snooze host", "Snooze a host until a time (until, RFC 3339) or for a time (for, 5m to 30d). A snoozed host sends no notifications. With undo, remove the snooze. Needs the scope alerts:write and the role member. The result has change.", true), s.snoozeHost)
	addTool(s, writeTool("mute_metric", "Mute metric", "Mute a metric on one host, or on the whole organization when host is empty. A muted metric makes no alerts. Set muted to false to unmute. A host mute needs the scope alerts:write and the role member. An organization mute needs the scope rules:write and the role admin, and it saves a rules backup (change.backup_id).", true), s.muteMetric)
	addTool(s, writeTool("set_thresholds", "Set alert limits", "Set the warn and critical limits of a metric, the sustain time and the notify level, on the organization or on one host. cpu, mem, disk, pids, files and inodes are ratios from 0 to 1. A host with clear follows the organization again. With cooldown_seconds, set the organization cooldown between two notifications. Needs the scope rules:write and the role admin. It saves a rules backup (change.backup_id). The result has the limits that apply now.", true), s.setThresholds)
	addTool(s, writeTool("ignore_process", "Ignore process", "Add a process name to the ignore list of the long-process alert, on one host or on all hosts. Give name (and host), or the alert_id of a long_process alert. With undo, remove the name. Needs the scope rules:write and the role admin. It saves a rules backup (change.backup_id).", true), s.ignoreProcess)
	addTool(s, writeTool("undo_rules_change", "Undo rules change", "Restore the rules from the backup of an earlier rules change (change.backup_id). It refuses when a rule changed after the backup, and it restores nothing then. The restore saves its own backup. Needs the scope rules:write and the role admin.", false), s.undoRulesChange)
	return s
}

// addTool registers the tool on the server of each tool set and in the
// registry of ToolSetLatest. The sets and the registry share one input
// schema. A schema that the tool already has stays.
func addTool[In, Out any](s *Server, t *sdk.Tool, h sdk.ToolHandlerFor[In, Out]) {
	schema, _ := t.InputSchema.(*jsonschema.Schema)
	if schema == nil {
		var err error
		if schema, err = jsonschema.For[In](&jsonschema.ForOptions{}); err != nil {
			panic(fmt.Sprintf("mcp: tool %q: input schema: %v", t.Name, err))
		}
		t.InputSchema = schema
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		panic(fmt.Sprintf("mcp: tool %q: input schema: %v", t.Name, err))
	}
	for _, set := range toolSets {
		ss := s.sets[set]
		sdk.AddTool(ss.srv, t, h)
		ss.order = append(ss.order, t.Name)
	}
	s.register(newToolEntry(t, schema, resolved, h))
}

// readTool defines a tool that reads and changes nothing.
func readTool(name, title, description string) *sdk.Tool {
	return newTool(name, title, description, sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true})
}

// writeTool defines a tool that changes data, but never deletes it.
func writeTool(name, title, description string, idempotent bool) *sdk.Tool {
	return newTool(name, title, description, sdk.ToolAnnotations{IdempotentHint: idempotent})
}

// newTool sets every hint, so no client uses a spec default.
func newTool(name, title, description string, a sdk.ToolAnnotations) *sdk.Tool {
	no := false
	a.Title = title
	a.DestructiveHint = &no
	a.OpenWorldHint = &no
	return &sdk.Tool{Name: name, Title: title, Description: description, Annotations: &a}
}

// Tools gives the tool definitions of the set in registration order.
func (s *Server) Tools(set ToolSet) []*sdk.Tool {
	ss := s.sets[set]
	if ss == nil {
		return nil
	}
	ctx := context.Background()
	serverT, clientT := sdk.NewInMemoryTransports()
	sess, err := ss.srv.Connect(ctx, serverT, nil)
	if err != nil {
		s.log.Error("mcp: list tools", "err", err)
		return nil
	}
	defer sess.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "towertail", Version: "tools"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		s.log.Error("mcp: list tools", "err", err)
		return nil
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		s.log.Error("mcp: list tools", "err", err)
		return nil
	}
	slices.SortStableFunc(res.Tools, func(a, b *sdk.Tool) int {
		return slices.Index(ss.order, a.Name) - slices.Index(ss.order, b.Name)
	})
	return res.Tools
}

// Handler serves the streamable HTTP transport of the tool set with no
// session state and JSON replies. The caller must verify the bearer token
// and put it on the request context with authctx.WithToken.
func (s *Server) Handler(set ToolSet) http.Handler {
	ss := s.sets[set]
	if ss == nil {
		panic("mcp: unknown tool set " + string(set))
	}
	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return ss.srv }, &sdk.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// Each request needs a bearer token, so DNS rebinding reads nothing.
		// A proxy on the same host would get 403 with the default.
		DisableLocalhostProtection: true,
	})
}

// inputError is a mistake in the arguments of a tool call. The model
// sees the text and can fix the call.
type inputError struct{ msg string }

func (e *inputError) Error() string { return e.msg }

func badf(format string, args ...any) error {
	return &inputError{msg: fmt.Sprintf(format, args...)}
}

// caller is the token and the role of a tool call.
type caller struct {
	Token store.Token
	Role  store.Role
}

// callerOf gives the token and the role on the context. A call with no
// role has the viewer role.
func callerOf(ctx context.Context) (caller, error) {
	t, ok := authctx.FromContext(ctx)
	if !ok {
		return caller{}, errors.New("no token")
	}
	role, ok := authctx.RoleFromContext(ctx)
	if !ok {
		role = store.RoleViewer
	}
	return caller{Token: t, Role: role}, nil
}

// toolError gives the text of an error that a model can read. It hides
// the text of other errors, and logs them.
func (s *Server) toolError(tool string, err error) error {
	var in *inputError
	var right *rightError
	switch {
	case errors.As(err, &in),
		errors.As(err, &right),
		errors.Is(err, query.ErrNoHistory),
		errors.Is(err, query.ErrBadOrder),
		errors.Is(err, query.ErrBadWindow),
		errors.Is(err, query.ErrBadDays),
		errors.Is(err, query.ErrHostNotFound),
		errors.Is(err, query.ErrHostAmbiguous),
		errors.Is(err, query.ErrProcessAmbiguous),
		errors.Is(err, query.ErrBadAlertQuery),
		errors.Is(err, query.ErrBadMetric),
		errors.Is(err, query.ErrBadRange),
		errors.Is(err, clickhouse.ErrUnknownMetric),
		errors.Is(err, clickhouse.ErrNoRollup):
		return err
	case errors.Is(err, store.ErrNotFound):
		if tool == "get_alert" {
			return errors.New("alert not found")
		}
		return errors.New("host not found")
	case errors.Is(err, clickhouse.ErrNoAlert):
		return errors.New("alert not found")
	case errors.Is(err, query.ErrProcessNotFound):
		return errors.New("process not found")
	case errors.Is(err, query.ErrBadSort):
		return errors.New("sort must be cpu, mem, disk or net")
	}
	s.log.Error("mcp: tool failed", "tool", tool, "err", err)
	return errors.New("the query failed")
}
