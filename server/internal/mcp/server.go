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

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// Queries is the part of the query service that the tools read.
type Queries interface {
	Hosts(ctx context.Context, orgID uuid.UUID, sort query.HostSort, limit int) (wire.FleetHosts, error)
	HostNames(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]string, error)
	ResolveHost(ctx context.Context, orgID uuid.UUID, ref string) (store.Node, error)
	History(ctx context.Context, r query.HistoryRequest) ([]clickhouse.HistoryPoint, time.Duration, bool, error)
	TopProcesses(ctx context.Context, q query.ProcQuery) (wire.FleetProcesses, error)
	Alerts(ctx context.Context, r query.AlertsRequest) (wire.AlertList, error)
	Disks(ctx context.Context, orgID, nodeID uuid.UUID, from, to time.Time) (query.DiskResult, error)
	Compare(ctx context.Context, q query.CompareQuery) (wire.Compare, error)
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

// Options holds the dependencies of the server.
type Options struct {
	Version string
	Log     *slog.Logger
}

// setServer is the MCP server of one tool set.
type setServer struct {
	srv   *sdk.Server
	order []string
}

// Server holds the MCP servers of the tool sets and their tools.
type Server struct {
	q    Queries
	sets map[ToolSet]*setServer
	log  *slog.Logger
	now  func() time.Time
}

// New makes the server with its tools.
func New(q Queries, o Options) *Server {
	s := &Server{
		q:    q,
		sets: make(map[ToolSet]*setServer, len(toolSets)),
		log:  o.Log,
		now:  time.Now,
	}
	for _, set := range toolSets {
		s.sets[set] = &setServer{srv: sdk.NewServer(
			&sdk.Implementation{Name: "towertail", Version: o.Version},
			&sdk.ServerOptions{Instructions: instructions},
		)}
	}
	// A handler returns a nil result, or leaves Content nil. Then the SDK
	// adds the JSON text block of the output. Google reads only that block.
	addTool(s, readTool("list_hosts", "List hosts", "List the hosts of the fleet with the latest CPU (percent), memory and disk (ratio 0 to 1), load and network (bytes each second). Offline hosts have no values."), s.listHosts)
	addTool(s, readTool("host_metrics", "Host metrics", "Get the history of metrics of one host: min, avg, max, last, and at most 300 points for each metric. The plan can shorten the range (clamped)."), s.hostMetrics)
	addTool(s, readTool("top_processes", "Top processes", "Get the top processes of one host, or of the fleet, by CPU or memory over a range."), s.topProcesses)
	addTool(s, readTool("alerts", "Alerts", "List the open alerts, or the recent alerts, of the fleet or of one host."), s.alerts)
	addTool(s, readTool("disks", "Disk usage", "Get the use of each disk mount of one host and the change over a range."), s.disks)
	addTool(s, readTool("compare", "Compare hosts", "Compare a metric of each host in the last window with the window before it."), s.compare)
	return s
}

// addTool registers the tool on the server of each tool set.
func addTool[In, Out any](s *Server, t *sdk.Tool, h sdk.ToolHandlerFor[In, Out]) {
	for _, set := range toolSets {
		ss := s.sets[set]
		sdk.AddTool(ss.srv, t, h)
		ss.order = append(ss.order, t.Name)
	}
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

// org gives the org of the token on the context.
func org(ctx context.Context) (uuid.UUID, error) {
	t, ok := authctx.FromContext(ctx)
	if !ok {
		return uuid.Nil, errors.New("no token")
	}
	return t.OrgID, nil
}

// toolError gives the text of an error that a model can read. It hides
// the text of other errors, and logs them.
func (s *Server) toolError(tool string, err error) error {
	var in *inputError
	switch {
	case errors.As(err, &in),
		errors.Is(err, query.ErrNoHistory),
		errors.Is(err, query.ErrBadOrder),
		errors.Is(err, query.ErrBadWindow),
		errors.Is(err, query.ErrBadDays),
		errors.Is(err, query.ErrHostNotFound),
		errors.Is(err, query.ErrHostAmbiguous),
		errors.Is(err, clickhouse.ErrUnknownMetric),
		errors.Is(err, clickhouse.ErrNoRollup):
		return err
	case errors.Is(err, query.ErrBadSort):
		return errors.New("sort must be cpu, mem, disk or net")
	}
	s.log.Error("mcp: tool failed", "tool", tool, "err", err)
	return errors.New("the query failed")
}
