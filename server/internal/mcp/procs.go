package mcp

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

// TopProcessesIn is the input of top_processes.
type TopProcessesIn struct {
	Host  string `json:"host,omitempty" jsonschema:"host ID or name. Empty means the whole fleet."`
	By    string `json:"by,omitempty" jsonschema:"order: cpu (average CPU), mem (peak memory) or io (average disk I/O). Default cpu."`
	Range string `json:"range,omitempty" jsonschema:"span up to now, 15m to 365d, for example 1h, 24h or 7d. Default 1h. Do not use with from or at."`
	From  string `json:"from,omitempty" jsonschema:"start of the range in RFC 3339. Use it with to, instead of range."`
	To    string `json:"to,omitempty" jsonschema:"end of the range in RFC 3339. Default now."`
	At    string `json:"at,omitempty" jsonschema:"one point in time instead of a range: RFC 3339, now or now-<span>, for example now-2h. Needs host. Gives PID, user and state for a time in the last hour."`
	Limit int    `json:"limit,omitempty" jsonschema:"most processes to return, 1 to 50. Default 20."`
}

// Process is one process of a host over a range, or at one time. PID,
// User and State are set for a time in the last hour. StartedAt is the
// start time that the sampler read, and StartExact is true then. Without
// it, FirstSeen is the oldest stored time, so the process is older than
// that. RunSeconds is the time from StartedAt to the end of the range, or
// to the time of at.
type Process struct {
	Host        string  `json:"host"`
	HostID      string  `json:"host_id"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Command     string  `json:"command"`
	Repo        string  `json:"repo,omitempty"`
	CPUPctAvg   float64 `json:"cpu_pct_avg"`
	CPUPctMax   float64 `json:"cpu_pct_max"`
	RSSMaxBytes uint64  `json:"rss_max_bytes"`
	IOBpsAvg    float64 `json:"io_bps_avg,omitempty"`
	PID         int32   `json:"pid,omitempty"`
	User        string  `json:"user,omitempty"`
	State       string  `json:"state,omitempty"`
	StartedAt   string  `json:"started_at,omitempty"`
	FirstSeen   string  `json:"first_seen,omitempty"`
	RunSeconds  int64   `json:"run_seconds,omitempty"`
	StartExact  bool    `json:"start_exact"`
}

// TopProcessesOut is the output of top_processes. A call with at gives At
// and Tier (raw, 1m or 1h) and no From and To.
type TopProcessesOut struct {
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	At      string `json:"at,omitempty"`
	Tier    string `json:"tier,omitempty"`
	By      string `json:"by"`
	Clamped bool   `json:"clamped,omitempty"`
	// Processes come in the order of By.
	Processes []Process `json:"processes"`
}

func (s *Server) topProcesses(ctx context.Context, _ *sdk.CallToolRequest, in TopProcessesIn) (*sdk.CallToolResult, TopProcessesOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, TopProcessesOut{}, err
	}
	orgID := c.Token.OrgID
	by, err := query.ParseProcOrder(in.By)
	if err != nil {
		return nil, TopProcessesOut{}, s.toolError("top_processes", err)
	}
	if in.Limit < 0 || in.Limit > query.MaxProcs {
		return nil, TopProcessesOut{}, badf("limit must be 1 to %d", query.MaxProcs)
	}
	now := s.now().UTC()
	var (
		out   TopProcessesOut
		procs []Process
		keys  []clickhouse.ProcStartKey
		end   time.Time
	)
	if strings.TrimSpace(in.At) != "" {
		if in.Range != "" || in.From != "" || in.To != "" {
			return nil, TopProcessesOut{}, badf("use at, or range, or from and to, but not together")
		}
		at, err := parseAt(in.At, now)
		if err != nil {
			return nil, TopProcessesOut{}, err
		}
		if strings.TrimSpace(in.Host) == "" {
			return nil, TopProcessesOut{}, badf("at needs host")
		}
		node, err := s.q.ResolveHost(ctx, orgID, in.Host)
		if err != nil {
			return nil, TopProcessesOut{}, s.toolError("top_processes", err)
		}
		snap, err := s.q.ProcessesAt(ctx, query.SnapshotRequest{OrgID: orgID, NodeID: node.ID, At: at, By: by, Limit: in.Limit})
		if err != nil {
			return nil, TopProcessesOut{}, s.toolError("top_processes", err)
		}
		out = TopProcessesOut{At: ts(snap.TS), Tier: snap.Tier, By: snap.By, Clamped: snap.Clamped}
		end = snap.TS
		for _, p := range snap.Processes {
			sp := Process{
				Host: node.DisplayName, HostID: node.ID.String(), Key: p.Key.String(), Name: p.Name, Command: p.Cmd, Repo: p.Repo,
				CPUPctAvg: p.CPUPct, CPUPctMax: max(p.CPUPct, p.CPUPctMax), RSSMaxBytes: p.RSS, IOBpsAvg: p.IOBps,
				PID: p.PID, User: p.User, State: p.State,
			}
			if p.StartTS != nil {
				sp.setStart(p.StartTS, end)
			} else {
				keys = append(keys, clickhouse.ProcStartKey{NodeID: node.ID, Key: p.Key})
			}
			procs = append(procs, sp)
		}
	} else {
		from, to, err := parseRange(in.Range, in.From, in.To, now)
		if err != nil {
			return nil, TopProcessesOut{}, err
		}
		node, err := s.hostID(ctx, orgID, in.Host)
		if err != nil {
			return nil, TopProcessesOut{}, s.toolError("top_processes", err)
		}
		res, err := s.q.TopProcesses(ctx, query.ProcQuery{OrgID: orgID, NodeID: node, From: from, To: to, By: by, Limit: in.Limit})
		if err != nil {
			return nil, TopProcessesOut{}, s.toolError("top_processes", err)
		}
		out = TopProcessesOut{From: ts(res.From), To: ts(res.To), By: res.By, Clamped: res.Clamped}
		end = res.To
		for _, p := range res.Processes {
			procs = append(procs, Process{
				Host: p.NodeName, HostID: p.NodeID.String(), Key: p.Key.String(), Name: p.Name, Command: p.Cmd, Repo: p.Repo,
				CPUPctAvg: p.CPUPctAvg, CPUPctMax: p.CPUPctMax, RSSMaxBytes: p.RSSMax, IOBpsAvg: p.IOBpsAvg,
			})
			keys = append(keys, clickhouse.ProcStartKey{NodeID: p.NodeID, Key: p.Key})
		}
	}
	if len(keys) > 0 {
		starts, err := s.q.ProcessStarts(ctx, orgID, keys, end)
		if err != nil {
			return nil, TopProcessesOut{}, s.toolError("top_processes", err)
		}
		for i := range procs {
			id, err := uuid.Parse(procs[i].HostID)
			if err != nil || procs[i].StartExact {
				continue
			}
			key, err := wire.ParseProcessKey(procs[i].Key)
			if err != nil {
				continue
			}
			if st, ok := starts[clickhouse.ProcStartKey{NodeID: id, Key: key}]; ok {
				procs[i].addStart(st, end)
			}
		}
	}
	out.Processes = procs
	if out.Processes == nil {
		out.Processes = []Process{}
	}
	return nil, out, nil
}

// setStart sets an exact start time and the run time at end.
func (p *Process) setStart(start *time.Time, end time.Time) {
	p.StartedAt, p.StartExact = ts(*start), true
	p.RunSeconds = max(int64(end.Sub(*start)/time.Second), 0)
}

// addStart sets the start facts of the query service.
func (p *Process) addStart(st query.ProcStart, end time.Time) {
	if p.PID == 0 {
		p.PID = st.PID
	}
	if st.Exact && st.StartedAt != nil {
		p.setStart(st.StartedAt, end)
		return
	}
	p.FirstSeen = tsPtr(st.FirstSeen)
}

// ProcessHistoryIn is the input of process_history.
type ProcessHistoryIn struct {
	Host    string `json:"host" jsonschema:"host ID or name"`
	Process string `json:"process" jsonschema:"the key of a process from top_processes, or a process name. A name that has more than one command gives an error with the keys."`
	Range   string `json:"range,omitempty" jsonschema:"span up to now, 15m to 365d, for example 1h, 24h or 7d. Default 1h. Do not use with from."`
	From    string `json:"from,omitempty" jsonschema:"start of the range in RFC 3339. Use it with to, instead of range."`
	To      string `json:"to,omitempty" jsonschema:"end of the range in RFC 3339. Default now."`
}

// ProcPointOut is one bucket of a process.
type ProcPointOut struct {
	TS       string  `json:"ts"`
	CPUPct   float64 `json:"cpu_pct"`
	RSSBytes uint64  `json:"rss_bytes"`
	IOBps    float64 `json:"io_bps,omitempty"`
}

// ProcStats are the stats of the points of a range. CPUPctMax is the
// highest bucket average.
type ProcStats struct {
	CPUPctAvg   float64 `json:"cpu_pct_avg"`
	CPUPctMax   float64 `json:"cpu_pct_max"`
	RSSMaxBytes uint64  `json:"rss_max_bytes"`
	IOBpsAvg    float64 `json:"io_bps_avg"`
}

// ProcFactsOut are the facts of the newest raw row of a process. They are
// set only when the range ends in the last hour.
type ProcFactsOut struct {
	PID       int32    `json:"pid"`
	PPID      int32    `json:"ppid,omitempty"`
	User      string   `json:"user,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	State     string   `json:"state,omitempty"`
	Threads   int32    `json:"threads,omitempty"`
	ListenTCP []uint16 `json:"listen_tcp,omitempty"`
	ListenUDP []uint16 `json:"listen_udp,omitempty"`
}

// ProcessHistoryOut is the output of process_history. See Process for the
// start fields.
type ProcessHistoryOut struct {
	Host       string         `json:"host"`
	Key        string         `json:"key"`
	Name       string         `json:"name"`
	Command    string         `json:"command"`
	Repo       string         `json:"repo,omitempty"`
	From       string         `json:"from"`
	To         string         `json:"to"`
	Resolution string         `json:"resolution"`
	Clamped    bool           `json:"clamped,omitempty"`
	Stats      ProcStats      `json:"stats"`
	Points     []ProcPointOut `json:"points"`
	Facts      *ProcFactsOut  `json:"facts,omitempty"`
	StartedAt  string         `json:"started_at,omitempty"`
	FirstSeen  string         `json:"first_seen,omitempty"`
	RunSeconds int64          `json:"run_seconds,omitempty"`
	StartExact bool           `json:"start_exact"`
}

func (s *Server) processHistory(ctx context.Context, _ *sdk.CallToolRequest, in ProcessHistoryIn) (*sdk.CallToolResult, ProcessHistoryOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, ProcessHistoryOut{}, err
	}
	orgID := c.Token.OrgID
	if strings.TrimSpace(in.Host) == "" {
		return nil, ProcessHistoryOut{}, badf("host is required")
	}
	if strings.TrimSpace(in.Process) == "" {
		return nil, ProcessHistoryOut{}, badf("process is required")
	}
	from, to, err := parseRange(in.Range, in.From, in.To, s.now().UTC())
	if err != nil {
		return nil, ProcessHistoryOut{}, err
	}
	node, err := s.q.ResolveHost(ctx, orgID, in.Host)
	if err != nil {
		return nil, ProcessHistoryOut{}, s.toolError("process_history", err)
	}
	key, err := s.q.ResolveProcess(ctx, orgID, node.ID, in.Process, from, to)
	if err != nil {
		return nil, ProcessHistoryOut{}, s.toolError("process_history", err)
	}
	h, err := s.q.ProcessHistory(ctx, query.ProcessHistoryRequest{OrgID: orgID, NodeID: node.ID, Key: key, From: from, To: to, MaxPoints: maxSeriesPoints})
	if err != nil {
		return nil, ProcessHistoryOut{}, s.toolError("process_history", err)
	}
	out := ProcessHistoryOut{
		Host: node.DisplayName, Key: key.String(), Name: key.Name, Command: h.Cmd, Repo: h.Repo,
		From: ts(h.From), To: ts(h.To), Resolution: resolutionText(h.Step), Clamped: h.Clamped,
		Stats: procStats(h.Points), Points: procPoints(h.Points, maxSeriesPoints),
	}
	if f := h.Facts; f != nil {
		out.Facts = &ProcFactsOut{
			PID: f.PID, PPID: f.PPID, User: f.User, Cwd: f.Cwd, State: f.State, Threads: f.Threads, ListenTCP: f.ListenTCP, ListenUDP: f.ListenUDP,
		}
	}
	p := Process{}
	p.addStart(h.Start, h.To)
	out.StartedAt, out.FirstSeen, out.RunSeconds, out.StartExact = p.StartedAt, p.FirstSeen, p.RunSeconds, p.StartExact
	return nil, out, nil
}

func procStats(pts []clickhouse.ProcPoint) ProcStats {
	var st ProcStats
	for _, p := range pts {
		st.CPUPctAvg += p.CPUPct / float64(len(pts))
		st.IOBpsAvg += p.IOBps / float64(len(pts))
		st.CPUPctMax = math.Max(st.CPUPctMax, p.CPUPct)
		st.RSSMaxBytes = max(st.RSSMaxBytes, p.RSS)
	}
	return st
}

// procPoints gives at most limit points. It merges neighbors: the average
// of CPU and I/O, and the highest memory.
func procPoints(pts []clickhouse.ProcPoint, limit int) []ProcPointOut {
	out := make([]ProcPointOut, 0, min(len(pts), limit))
	size := max((len(pts)+limit-1)/limit, 1)
	for i := 0; i < len(pts); i += size {
		group := pts[i:min(i+size, len(pts))]
		p := ProcPointOut{TS: ts(group[0].TS)}
		for _, g := range group {
			p.CPUPct += g.CPUPct / float64(len(group))
			p.IOBps += g.IOBps / float64(len(group))
			p.RSSBytes = max(p.RSSBytes, g.RSS)
		}
		out = append(out, p)
	}
	return out
}
