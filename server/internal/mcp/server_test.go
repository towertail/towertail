package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

var update = flag.Bool("update", false, "write the golden files again")

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// fakeQueries gives fixed data and records the org and the last
// requests.
type fakeQueries struct {
	orgs    []uuid.UUID
	nodes   []store.Node
	points  []clickhouse.HistoryPoint
	gotHist query.HistoryRequest
	gotProc query.ProcQuery
	gotAlrt query.AlertsRequest
	gotCmp  query.CompareQuery
}

func (f *fakeQueries) seen(org uuid.UUID) { f.orgs = append(f.orgs, org) }

func (f *fakeQueries) Hosts(_ context.Context, org uuid.UUID, sort query.HostSort, limit int) (wire.FleetHosts, error) {
	f.seen(org)
	cpu := 42.5
	var out wire.FleetHosts
	for _, n := range f.nodes {
		out.Hosts = append(out.Hosts, wire.FleetHost{NodeID: n.ID, Name: n.DisplayName, Online: true, LastSeen: &now, CPU: &cpu})
	}
	return out, nil
}

func (f *fakeQueries) HostNames(_ context.Context, org uuid.UUID) (map[uuid.UUID]string, error) {
	f.seen(org)
	m := map[uuid.UUID]string{}
	for _, n := range f.nodes {
		m[n.ID] = n.DisplayName
	}
	return m, nil
}

func (f *fakeQueries) ResolveHost(_ context.Context, org uuid.UUID, ref string) (store.Node, error) {
	f.seen(org)
	for _, n := range f.nodes {
		if n.DisplayName == ref || n.ID.String() == ref {
			return n, nil
		}
	}
	return store.Node{}, query.ErrHostNotFound
}

func (f *fakeQueries) History(_ context.Context, r query.HistoryRequest) ([]clickhouse.HistoryPoint, time.Duration, bool, error) {
	f.seen(r.OrgID)
	f.gotHist = r
	return f.points, time.Minute, true, nil
}

func (f *fakeQueries) TopProcesses(_ context.Context, q query.ProcQuery) (wire.FleetProcesses, error) {
	f.seen(q.OrgID)
	f.gotProc = q
	return wire.FleetProcesses{From: q.From, To: q.To, By: string(q.By), Processes: []wire.FleetProcess{
		{NodeID: f.nodes[0].ID, NodeName: f.nodes[0].DisplayName, Name: "postgres", Cmd: "postgres -D /data", CPUPctAvg: 80, CPUPctMax: 99, RSSMax: 1 << 30},
	}}, nil
}

func (f *fakeQueries) Alerts(_ context.Context, r query.AlertsRequest) (wire.AlertList, error) {
	f.seen(r.OrgID)
	f.gotAlrt = r
	return wire.AlertList{Alerts: []wire.Alert{
		{ID: uuid.New(), NodeID: f.nodes[0].ID, Kind: "threshold", Metric: "cpu", Tint: wire.Tint("crit"), TS: now, Value: 97},
	}}, nil
}

func (f *fakeQueries) Disks(_ context.Context, org, node uuid.UUID, from, to time.Time) (query.DiskResult, error) {
	f.seen(org)
	return query.DiskResult{From: from, To: to, Points: []clickhouse.DiskPoint{
		{Mount: "/", FS: "ext4", TS: from, Used: 40, Total: 100},
		{Mount: "/", FS: "ext4", TS: to, Used: 50, Total: 100},
		{Mount: "/data", FS: "xfs", TS: from, Used: 10, Total: 200},
		{Mount: "/data", FS: "xfs", TS: to, Used: 8, Total: 200},
	}}, nil
}

func (f *fakeQueries) Compare(_ context.Context, q query.CompareQuery) (wire.Compare, error) {
	f.seen(q.OrgID)
	f.gotCmp = q
	delta := 5.0
	return wire.Compare{
		Metric:   q.Metric,
		Current:  wire.Window{From: now.Add(-q.Window), To: now},
		Previous: wire.Window{From: now.Add(-2 * q.Window), To: now.Add(-q.Window)},
		Hosts: []wire.CompareHost{{
			NodeID: f.nodes[0].ID, Name: f.nodes[0].DisplayName,
			Current: wire.Stat{Avg: 30, Max: 60, Points: 10}, Previous: &wire.Stat{Avg: 25, Max: 50, Points: 10}, DeltaAvg: &delta,
		}},
	}, nil
}

func newFake() *fakeQueries {
	return &fakeQueries{nodes: []store.Node{{ID: uuid.New(), DisplayName: "web-1"}, {ID: uuid.New(), DisplayName: "db-1"}}}
}

// connect starts a client session. The server side gets the token on its
// context, as the HTTP route gives it.
func connect(t *testing.T, q Queries, token *store.Token) *sdk.ClientSession {
	t.Helper()
	return connectSet(t, newServer(q), ToolSetLatest, token)
}

func newServer(q Queries) *Server {
	s := New(q, Options{Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	s.now = func() time.Time { return now }
	return s
}

func connectSet(t *testing.T, s *Server, set ToolSet, token *store.Token) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	if token != nil {
		ctx = authctx.WithToken(ctx, *token)
	}
	serverT, clientT := sdk.NewInMemoryTransports()
	ss, err := s.sets[set].srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

func text(res *sdk.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// decode reads the structured output of a good call.
func decode[T any](t *testing.T, res *sdk.CallToolResult) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", text(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

var orgA = uuid.New()

func tokenA() *store.Token {
	return &store.Token{ID: uuid.New(), OrgID: orgA, Kind: store.TokenKindAPI}
}

func TestTools(t *testing.T) {
	f := newFake()
	cs := connect(t, f, tokenA())

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tool.Name)
		}
	}
	slices.Sort(names)
	want := []string{"alerts", "compare", "disks", "host_metrics", "list_hosts", "top_processes"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}

	t.Run("list_hosts", func(t *testing.T) {
		out := decode[ListHostsOut](t, call(t, cs, "list_hosts", map[string]any{"sort": "mem", "limit": 5}))
		if len(out.Hosts) != 2 || out.Hosts[0].Name != "web-1" || out.Hosts[0].CPUPct == nil || *out.Hosts[0].CPUPct != 42.5 || out.Hosts[0].LastSeen != "2026-11-20T12:00:00Z" {
			t.Fatalf("hosts = %+v", out.Hosts)
		}
	})
	t.Run("host_metrics", func(t *testing.T) {
		f.points = []clickhouse.HistoryPoint{{TS: now.Add(-2 * time.Minute), Value: 10, Max: 30}, {TS: now.Add(-time.Minute), Value: 20, Max: 25}}
		out := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"host": "web-1", "metrics": []string{"cpu", "mem"}, "range": "2h"}))
		if len(out.Metrics) != 2 || out.Host != "web-1" || !out.Clamped {
			t.Fatalf("out = %+v", out)
		}
		sr := out.Metrics[0]
		if sr.Metric != "cpu" || sr.Count != 2 || sr.Min != 10 || sr.Avg != 15 || sr.Max != 30 || sr.Last != 20 || len(sr.Points) != 2 {
			t.Fatalf("cpu series = %+v", sr)
		}
		if f.gotHist.OrgID != orgA || !f.gotHist.From.Equal(now.Add(-2*time.Hour)) || f.gotHist.Step < 24*time.Second {
			t.Fatalf("request = %+v", f.gotHist)
		}
	})
	t.Run("top_processes", func(t *testing.T) {
		out := decode[TopProcessesOut](t, call(t, cs, "top_processes", map[string]any{"host": "db-1", "by": "mem", "limit": 5}))
		if len(out.Processes) != 1 || out.Processes[0].Name != "postgres" || out.Processes[0].RSSMaxBytes != 1<<30 || out.By != "mem" {
			t.Fatalf("out = %+v", out)
		}
		if f.gotProc.NodeID != f.nodes[1].ID || f.gotProc.Limit != 5 || !f.gotProc.From.Equal(now.Add(-time.Hour)) {
			t.Fatalf("request = %+v", f.gotProc)
		}
		call(t, cs, "top_processes", nil)
		if f.gotProc.NodeID != uuid.Nil {
			t.Fatalf("empty host: node = %s, want the fleet", f.gotProc.NodeID)
		}
	})
	t.Run("alerts", func(t *testing.T) {
		out := decode[AlertsOut](t, call(t, cs, "alerts", map[string]any{"state": "recent", "days": 3, "host": "web-1"}))
		if len(out.Alerts) != 1 || out.Alerts[0].Host != "web-1" || out.Alerts[0].Tint != "crit" || out.Alerts[0].Start != "2026-11-20T12:00:00Z" {
			t.Fatalf("out = %+v", out)
		}
		if f.gotAlrt.Open || f.gotAlrt.Days != 3 || f.gotAlrt.NodeID != f.nodes[0].ID || f.gotAlrt.OrgID != orgA {
			t.Fatalf("request = %+v", f.gotAlrt)
		}
		decode[AlertsOut](t, call(t, cs, "alerts", nil))
		if !f.gotAlrt.Open {
			t.Fatal("default state is not open")
		}
	})
	t.Run("disks", func(t *testing.T) {
		out := decode[DisksOut](t, call(t, cs, "disks", map[string]any{"host": "web-1"}))
		if len(out.Mounts) != 2 {
			t.Fatalf("mounts = %+v", out.Mounts)
		}
		root, data := out.Mounts[0], out.Mounts[1]
		if root.Mount != "/" || root.UsedBytes != 50 || root.Ratio != 0.5 || root.ChangeBytes != 10 {
			t.Errorf("root = %+v", root)
		}
		if data.Mount != "/data" || data.ChangeBytes != -2 || data.FS != "xfs" {
			t.Errorf("data = %+v", data)
		}
		if out.From != "2026-11-19T12:00:00Z" {
			t.Errorf("default range: from = %s, want 24h", out.From)
		}
	})
	t.Run("compare", func(t *testing.T) {
		out := decode[CompareOut](t, call(t, cs, "compare", map[string]any{"metric": "mem", "window": "1d"}))
		if f.gotCmp.Window != 24*time.Hour || f.gotCmp.Metric != "mem" || f.gotCmp.NodeID != uuid.Nil {
			t.Fatalf("request = %+v", f.gotCmp)
		}
		if len(out.Hosts) != 1 || out.TotalHosts != 1 || out.Hosts[0].ChangeAvg == nil || *out.Hosts[0].ChangeAvg != 5 || out.Hosts[0].Previous == nil {
			t.Fatalf("out = %+v", out)
		}
	})
}

func TestToolInputErrors(t *testing.T) {
	cs := connect(t, newFake(), tokenA())
	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"range too long", "host_metrics", map[string]any{"host": "web-1", "range": "400d"}, "range must be 15m to 365d"},
		{"unknown metric", "host_metrics", map[string]any{"host": "web-1", "metrics": []string{"bogus"}}, "metric must be one of"},
		{"unknown host", "host_metrics", map[string]any{"host": "ghost"}, "host not found"},
		{"empty host", "disks", map[string]any{"host": ""}, "host is required"},
		{"resolution", "host_metrics", map[string]any{"host": "web-1", "resolution": "2m"}, "resolution must be"},
		{"sort", "list_hosts", map[string]any{"sort": "bogus"}, "sort must be"},
		{"order", "top_processes", map[string]any{"by": "disk"}, "by must be cpu or mem"},
		{"state", "alerts", map[string]any{"state": "closed"}, "state must be"},
		{"window", "compare", map[string]any{"window": "soon"}, "bad duration"},
		{"compare metric", "compare", map[string]any{"metric": "procs"}, "metric must be one of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, cs, tc.tool, tc.args)
			if !res.IsError || !strings.Contains(text(res), tc.want) {
				t.Fatalf("IsError = %v, text = %q; want a tool error with %q", res.IsError, text(res), tc.want)
			}
		})
	}
}

func TestNoToken(t *testing.T) {
	f := newFake()
	cs := connect(t, f, nil)
	res := call(t, cs, "list_hosts", nil)
	if !res.IsError || !strings.Contains(text(res), "no token") {
		t.Fatalf("IsError = %v, text = %q; want the tool error no token", res.IsError, text(res))
	}
	if len(f.orgs) != 0 {
		t.Fatal("a call with no token reached the queries")
	}
}

func TestHostMetricsPointCap(t *testing.T) {
	f := newFake()
	for i := range 2000 {
		f.points = append(f.points, clickhouse.HistoryPoint{TS: now.Add(-time.Duration(2000-i) * time.Second), Value: float64(i % 100)})
	}
	cs := connect(t, f, tokenA())
	out := decode[HostMetricsOut](t, call(t, cs, "host_metrics", map[string]any{"host": "web-1", "metrics": []string{"cpu", "mem"}, "range": "6h", "resolution": "raw"}))
	for _, sr := range out.Metrics {
		if len(sr.Points) == 0 || len(sr.Points) > 300 || sr.Count != 2000 {
			t.Errorf("%s: %d points, count %d; want 1 to 300 points, count 2000", sr.Metric, len(sr.Points), sr.Count)
		}
		if sr.Max != 99 || sr.Min != 0 {
			t.Errorf("%s: min %v max %v, want the stats of all points", sr.Metric, sr.Min, sr.Max)
		}
	}
	if f.gotHist.Step < 72*time.Second {
		t.Errorf("step = %v, want 72s or more for 6h", f.gotHist.Step)
	}
}

func TestMCPOrgScope(t *testing.T) {
	f := newFake()
	orgB := uuid.New()
	for _, org := range []uuid.UUID{orgA, orgB} {
		f.orgs = nil
		cs := connect(t, f, &store.Token{ID: uuid.New(), OrgID: org, Kind: store.TokenKindAPI})
		decode[ListHostsOut](t, call(t, cs, "list_hosts", nil))
		if len(f.orgs) != 1 || f.orgs[0] != org {
			t.Fatalf("orgs = %v, want only %s", f.orgs, org)
		}
	}
}

func TestToolAnnotations(t *testing.T) {
	s := newServer(newFake())
	for _, set := range toolSets {
		cs := connectSet(t, s, set, tokenA())
		list, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Tools) == 0 {
			t.Fatalf("%s: no tools", set)
		}
		for _, tool := range list.Tools {
			a := tool.Annotations
			if tool.Title == "" || a == nil || a.Title != tool.Title || a.DestructiveHint == nil || a.OpenWorldHint == nil {
				t.Errorf("%s/%s: title or hints are not complete: %+v", set, tool.Name, tool)
			}
		}
		raw, err := json.Marshal(s.Tools(set))
		if err != nil {
			t.Fatal(err)
		}
		var tools []struct {
			Annotations map[string]json.RawMessage `json:"annotations"`
		}
		if err := json.Unmarshal(raw, &tools); err != nil {
			t.Fatal(err)
		}
		for i, tool := range tools {
			for _, key := range []string{"title", "readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
				if _, ok := tool.Annotations[key]; !ok {
					t.Errorf("%s: tool %d: annotations has no %q", set, i, key)
				}
			}
		}
	}
}

func TestToolHints(t *testing.T) {
	r := readTool("r", "R", "d").Annotations
	if !r.ReadOnlyHint || !r.IdempotentHint || *r.DestructiveHint || *r.OpenWorldHint {
		t.Errorf("read hints = %+v", r)
	}
	for _, idem := range []bool{true, false} {
		w := writeTool("w", "W", "d", idem).Annotations
		if w.ReadOnlyHint || w.IdempotentHint != idem || *w.DestructiveHint || *w.OpenWorldHint {
			t.Errorf("write hints (idempotent %v) = %+v", idem, w)
		}
	}
}

func TestAddToolBothSets(t *testing.T) {
	s := newServer(newFake())
	type in struct{}
	type out struct{}
	addTool(s, readTool("extra_tool", "Extra", "d"), func(context.Context, *sdk.CallToolRequest, in) (*sdk.CallToolResult, out, error) {
		return nil, out{}, nil
	})
	for _, set := range toolSets {
		if !slices.ContainsFunc(s.Tools(set), func(tl *sdk.Tool) bool { return tl.Name == "extra_tool" }) {
			t.Errorf("%s has no extra_tool", set)
		}
	}
}

func TestToolsRegistrationOrder(t *testing.T) {
	var names []string
	for _, tl := range newServer(newFake()).Tools(ToolSetV1) {
		names = append(names, tl.Name)
	}
	want := []string{"list_hosts", "host_metrics", "top_processes", "alerts", "disks", "compare"}
	if !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestToolNames(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	s := newServer(newFake())
	for _, set := range toolSets {
		for _, tl := range s.Tools(set) {
			if !re.MatchString(tl.Name) || len(tl.Name) > 64 {
				t.Errorf("%s: bad tool name %q", set, tl.Name)
			}
		}
	}
}

func TestInstructions(t *testing.T) {
	s := newServer(newFake())
	for _, set := range toolSets {
		cs := connectSet(t, s, set, tokenA())
		got := cs.InitializeResult().Instructions
		if got == "" || len(got) > 2048 {
			t.Errorf("%s: instructions have %d bytes, want 1 to 2048", set, len(got))
		}
	}
}

func TestToolResultHasJSONText(t *testing.T) {
	f := newFake()
	f.points = []clickhouse.HistoryPoint{{TS: now.Add(-time.Minute), Value: 10, Max: 12}}
	cs := connect(t, f, tokenA())
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"list_hosts", nil},
		{"host_metrics", map[string]any{"host": "web-1"}},
		{"top_processes", nil},
		{"alerts", nil},
		{"disks", map[string]any{"host": "web-1"}},
		{"compare", nil},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			res := call(t, cs, tc.tool, tc.args)
			if res.IsError {
				t.Fatalf("tool error: %s", text(res))
			}
			block, ok := res.Content[0].(*sdk.TextContent)
			if !ok {
				t.Fatalf("Content[0] is %T, want *TextContent", res.Content[0])
			}
			structured, err := json.Marshal(res.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var a, b any
			if err := json.Unmarshal([]byte(block.Text), &a); err != nil {
				t.Fatalf("the text is not JSON: %v", err)
			}
			if err := json.Unmarshal(structured, &b); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("text %s differs from structured content %s", block.Text, structured)
			}
		})
	}
}

// TestV1ToolsGolden freezes the /mcp/v1 tool schemas. Run with -update to
// write the file again after a deliberate change.
func TestV1ToolsGolden(t *testing.T) {
	got, err := json.MarshalIndent(newServer(newFake()).Tools(ToolSetV1), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/tools_v1.json"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the v1 tools differ from %s. Run the test with -update if the change is on purpose.", path)
	}
}
