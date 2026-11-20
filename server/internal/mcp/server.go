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

// Server holds the MCP server and its tools.
type Server struct {
	q   Queries
	srv *sdk.Server
	log *slog.Logger
	now func() time.Time
}

// New makes the server with its six tools. version is the server version.
func New(q Queries, version string, log *slog.Logger) *Server {
	s := &Server{
		q:   q,
		srv: sdk.NewServer(&sdk.Implementation{Name: "towertail", Version: version}, nil),
		log: log,
		now: time.Now,
	}
	sdk.AddTool(s.srv, readOnly("list_hosts", "List the hosts of the fleet with the latest CPU (percent), memory and disk (ratio 0 to 1), load and network (bytes each second). Offline hosts have no values."), s.listHosts)
	sdk.AddTool(s.srv, readOnly("host_metrics", "Get the history of metrics of one host: min, avg, max, last, and at most 300 points for each metric. The plan can shorten the range (clamped)."), s.hostMetrics)
	sdk.AddTool(s.srv, readOnly("top_processes", "Get the top processes of one host, or of the fleet, by CPU or memory over a range."), s.topProcesses)
	sdk.AddTool(s.srv, readOnly("alerts", "List the open alerts, or the recent alerts, of the fleet or of one host."), s.alerts)
	sdk.AddTool(s.srv, readOnly("disks", "Get the use of each disk mount of one host and the change over a range."), s.disks)
	sdk.AddTool(s.srv, readOnly("compare", "Compare a metric of each host in the last window with the window before it."), s.compare)
	return s
}

func readOnly(name, description string) *sdk.Tool {
	closed := false
	return &sdk.Tool{
		Name:        name,
		Description: description,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closed},
	}
}

// Handler serves the streamable HTTP transport with no session state and
// JSON replies. The caller must verify the bearer token and put it on the
// request context with authctx.WithToken.
func (s *Server) Handler() http.Handler {
	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.srv }, &sdk.StreamableHTTPOptions{
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
