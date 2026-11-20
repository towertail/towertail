package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

const (
	rankTTL        = 60 * time.Second
	planRetryAfter = 3600
	samplerUAName  = "towertail-sampler/"
)

// ingestGate is the order of the server for one ingest request.
type ingestGate struct {
	gone     string
	planFull bool
	node     store.Node
}

// ingestGate reads the node and the plan rank of the sampler. A node
// that is gone or off gives gate.gone. A node above the plan limit gives
// gate.planFull.
func (s *Server) ingestGate(ctx context.Context, t store.Token) (gate ingestGate, err error) {
	node, err := s.store.GetNode(ctx, t.OrgID, *t.NodeID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		gate.gone = wire.GoneHostRemoved
		return gate, nil
	case err != nil:
		return gate, err
	}
	gate.node = node
	if !node.Enabled {
		gate.gone = wire.GoneCloudOff
		return gate, nil
	}
	rank, max, err := s.planRank(ctx, t.OrgID, node.ID)
	if err != nil {
		return gate, err
	}
	gate.planFull = max >= 0 && rank >= max
	return gate, nil
}

// planRank gives the place of the node among the nodes of the org, sorted
// by creation time then id. max is the node limit of the plan. A negative
// max means no limit.
func (s *Server) planRank(ctx context.Context, orgID, nodeID uuid.UUID) (rank, max int, err error) {
	if s.limits == nil {
		return 0, -1, nil
	}
	max = s.limits.MaxNodes(ctx, orgID)
	if max < 0 {
		return 0, -1, nil
	}
	rank, err = s.ranks.rank(ctx, s.store, orgID, nodeID)
	return rank, max, err
}

// rankCache keeps the node order of each org for a short time. It saves
// a node list on each ingest request.
type rankCache struct {
	mu   sync.Mutex
	orgs map[uuid.UUID]rankEntry
}

type rankEntry struct {
	at    time.Time
	ranks map[uuid.UUID]int
}

func newRankCache() *rankCache {
	return &rankCache{orgs: map[uuid.UUID]rankEntry{}}
}

func (c *rankCache) rank(ctx context.Context, st store.ConfigStore, orgID, nodeID uuid.UUID) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.orgs[orgID]
	if ok && time.Since(e.at) < rankTTL {
		if r, ok := e.ranks[nodeID]; ok {
			return r, nil
		}
	}
	nodes, err := st.ListNodes(ctx, orgID)
	if err != nil {
		return 0, err
	}
	slices.SortFunc(nodes, func(a, b store.Node) int {
		if d := a.CreatedAt.Compare(b.CreatedAt); d != 0 {
			return d
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	e = rankEntry{at: time.Now(), ranks: make(map[uuid.UUID]int, len(nodes))}
	for i, n := range nodes {
		e.ranks[n.ID] = i
	}
	c.orgs[orgID] = e
	r, ok := e.ranks[nodeID]
	if !ok {
		return len(nodes), nil
	}
	return r, nil
}

// drop forgets the order of the org. Call it when a node is made or
// deleted.
func (c *rankCache) drop(orgID uuid.UUID) {
	c.mu.Lock()
	delete(c.orgs, orgID)
	c.mu.Unlock()
}

// intervalFor gives the push interval order for the host, or "".
func (s *Server) intervalFor(orgID, nodeID uuid.UUID) string {
	if s.interval == nil {
		return ""
	}
	if d := s.interval.Interval(orgID, nodeID); d > 0 {
		return d.String()
	}
	return ""
}

// updateFor gives the configured latest sampler version when the sampler
// in the User-Agent header is older. Else it gives "".
func (s *Server) updateFor(userAgent string) string {
	latest, ok := parseSemver(s.cfg.Sampler.LatestVersion)
	if !ok {
		return ""
	}
	rest, found := strings.CutPrefix(userAgent, samplerUAName)
	if !found {
		return ""
	}
	rest, _, _ = strings.Cut(rest, " ")
	cur, ok := parseSemver(rest)
	if !ok || slices.Compare(cur[:], latest[:]) >= 0 {
		return ""
	}
	return s.cfg.Sampler.LatestVersion
}

// parseSemver reads MAJOR.MINOR.PATCH. A "v" prefix and a "-" or "+"
// suffix are ignored.
func parseSemver(v string) (out [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// reply writes the 410 or 402 order. It reports whether it wrote one.
func (g ingestGate) reply(c echo.Context) (bool, error) {
	switch {
	case g.gone != "":
		ingestRequests.WithLabelValues("410").Inc()
		return true, c.JSON(http.StatusGone, wire.IngestGone{Action: "exit", Reason: g.gone})
	case g.planFull:
		ingestRequests.WithLabelValues("402").Inc()
		c.Response().Header().Set("Retry-After", strconv.Itoa(planRetryAfter))
		return true, c.JSON(http.StatusPaymentRequired, wire.IngestPlanFull{RetryAfter: planRetryAfter})
	}
	return false, nil
}
