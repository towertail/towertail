package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// ErrLegacyClient is the error of SetView for a client that did not
// connect with ?lease=1.
var ErrLegacyClient = errors.New("hub: view frame on a legacy client")

// Options sets the behavior of a Hub.
type Options struct {
	// LegacySamples keeps the full sample frames for clients without
	// ?lease=1. When false, those clients get events only.
	LegacySamples bool
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// ClientOptions sets the mode of a client.
type ClientOptions struct {
	Lease bool
	// UserKey is the user of the token, else the token.
	UserKey uuid.UUID
}

// Hub fans out samples and events to connected websocket clients.
// Connections are grouped by org. Slow consumers are dropped rather
// than being allowed to backpressure the fast path.
type Hub struct {
	log  *slog.Logger
	opts Options

	// mu guards clients and nodes. Lock order: mu, then Client.mu.
	mu      sync.RWMutex
	clients map[uuid.UUID]map[*Client]struct{}
	nodes   map[uuid.UUID]map[uuid.UUID]*nodeState // orgID → nodeID
}

// nodeState is what the hub keeps for each node.
type nodeState struct {
	last      *sampleFrames
	lastPorts *sampleFrames // the last sample that has ports
	facts     wire.HostFacts
	health    []byte
}

type Client struct {
	id      uuid.UUID
	orgID   uuid.UUID
	conn    *websocket.Conn
	send    chan []byte
	log     *slog.Logger
	hub     *Hub
	lease   bool
	userKey uuid.UUID

	mu     sync.Mutex
	subset map[uuid.UUID]struct{}
	view   *lease
}

func New(log *slog.Logger, o Options) *Hub {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Hub{
		log:     log,
		opts:    o,
		clients: map[uuid.UUID]map[*Client]struct{}{},
		nodes:   map[uuid.UUID]map[uuid.UUID]*nodeState{},
	}
}

// Register installs a new client for the given org and returns a handle
// the caller uses to feed inbound frames.
func (h *Hub) Register(orgID uuid.UUID, conn *websocket.Conn, o ClientOptions) *Client {
	c := &Client{
		id:      uuid.New(),
		orgID:   orgID,
		conn:    conn,
		send:    make(chan []byte, 128),
		log:     h.log,
		hub:     h,
		lease:   o.Lease,
		userKey: o.UserKey,
	}
	h.mu.Lock()
	if _, ok := h.clients[orgID]; !ok {
		h.clients[orgID] = map[*Client]struct{}{}
	}
	h.clients[orgID][c] = struct{}{}
	h.mu.Unlock()
	streamClients.WithLabelValues(modeLabel(c.lease)).Inc()
	return c
}

// Unregister removes the client from its org set, drops its lease and
// closes the send channel.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if set, ok := h.clients[c.orgID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, c.orgID)
		}
	}
	h.mu.Unlock()
	c.swapView(nil)
	streamClients.WithLabelValues(modeLabel(c.lease)).Dec()
	close(c.send)
}

// PublishSample fans a sample out to the clients of the org. A legacy
// client gets the raw sample. A lease client gets the summary of a host
// that its lease covers, and the detail of its open tab. The hub also
// keeps the frames of the sample for late joiners.
func (h *Hub) PublishSample(orgID, nodeID uuid.UUID, s wire.Sample, raw json.RawMessage) {
	if !h.opts.LegacySamples {
		raw = nil
	}
	h.mu.Lock()
	ns := h.nodeStateLocked(orgID, nodeID)
	facts := s.Facts()
	var health []byte
	if sum := s.Summary(); sum.Health != nil {
		health, _ = json.Marshal(sum.Health)
	}
	frames, err := newSampleFrames(nodeID, s, raw,
		!ns.facts.Same(facts), health != nil && !bytes.Equal(health, ns.health))
	if err != nil {
		h.mu.Unlock()
		h.log.Error("hub: marshal sample", "err", err)
		return
	}
	ns.last, ns.facts = frames, facts
	if health != nil {
		ns.health = health
	}
	if s.Ports != nil {
		ns.lastPorts = frames
	}
	h.mu.Unlock()

	now := h.now()
	h.broadcast(orgID, func(c *Client, emit func(string, []byte)) {
		if !c.lease {
			if h.opts.LegacySamples && c.subscribed(nodeID) {
				emit("sample", frames.legacy())
			}
			return
		}
		view := c.liveView(now)
		if !view.covers(nodeID, now) {
			return
		}
		emit("summary", frames.summaryFrame(false))
		if tab, ok := view.wantsDetail(nodeID, now); ok {
			if data, ok := frames.detailFrame(tab); ok {
				emit("detail", data)
			}
		}
	})
}

func (h *Hub) nodeStateLocked(orgID, nodeID uuid.UUID) *nodeState {
	byNode, ok := h.nodes[orgID]
	if !ok {
		byNode = map[uuid.UUID]*nodeState{}
		h.nodes[orgID] = byNode
	}
	ns, ok := byNode[nodeID]
	if !ok {
		ns = &nodeState{}
		byNode[nodeID] = ns
	}
	return ns
}

// Publish (EventSink satisfied) pushes an event to all clients in the org.
func (h *Hub) Publish(orgID uuid.UUID, e wire.Event) {
	msg := wire.WSMessage{Type: "event", Event: &e}
	data, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("hub: marshal event", "err", err)
		return
	}
	h.broadcastTo(orgID, "event", data, &e.NodeID)
}

// PublishDiskScan sends disk_scan_updated to the clients of the org
// that see the node of the job.
func (h *Hub) PublishDiskScan(orgID uuid.UUID, job wire.DiskScanJob) {
	nodeID := job.NodeID
	msg := wire.WSMessage{Type: "disk_scan_updated", NodeID: &nodeID, Job: &job}
	data, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("hub: marshal disk scan", "err", err)
		return
	}
	h.broadcastTo(orgID, msg.Type, data, &nodeID)
}

// BroadcastOrg sends a pre-serialised frame to every client in the
// given org. Used by the API layer for node_updated / settings_updated.
func (h *Hub) BroadcastOrg(orgID uuid.UUID, msg wire.WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("hub: marshal", "err", err)
		return
	}
	h.broadcastTo(orgID, msg.Type, data, nil)
}

// broadcastTo sends one frame to each client that sees the node. A lease
// client sees all nodes. A legacy client has the subset filter.
func (h *Hub) broadcastTo(orgID uuid.UUID, typ string, data []byte, nodeID *uuid.UUID) {
	h.broadcast(orgID, func(c *Client, emit func(string, []byte)) {
		if nodeID == nil || c.lease || c.subscribed(*nodeID) {
			emit(typ, data)
		}
	})
}

// broadcast calls pick for each client of the org. pick emits the
// frames for the client. A client with a full send channel is closed.
func (h *Hub) broadcast(orgID uuid.UUID, pick func(c *Client, emit func(typ string, data []byte))) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients[orgID]))
	for c := range h.clients[orgID] {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		dropped := false
		pick(c, func(typ string, data []byte) {
			if dropped {
				return
			}
			if !h.enqueue(c, typ, data) {
				dropped = true
				h.log.Warn("hub: slow consumer dropped", "client", c.id)
				if c.conn != nil {
					_ = c.conn.Close(websocket.StatusPolicyViolation, "slow consumer")
				}
			}
		})
	}
}

// enqueue puts a frame on the send channel. It returns false when the
// channel is full.
func (h *Hub) enqueue(c *Client, typ string, data []byte) bool {
	select {
	case c.send <- data:
		streamFrames.WithLabelValues(typ).Inc()
		streamFrameBytes.WithLabelValues(typ).Add(float64(len(data)))
		return true
	default:
		return false
	}
}

// ReplayFor sends the first frames of a connection. A legacy client gets
// the last sample of each node, so a reconnect paints at once. A lease
// client gets the hello frame; it asks for data with a view frame.
func (h *Hub) ReplayFor(c *Client) {
	if c.lease {
		hello := wire.WSMessage{Type: "hello", Hello: &wire.StreamHello{
			Lease:   true,
			RenewS:  int(renewEvery / time.Second),
			MaxTTLS: int(maxTTL / time.Second),
		}}
		if data, err := json.Marshal(hello); err == nil {
			h.enqueue(c, "hello", data)
		}
		return
	}
	if !h.opts.LegacySamples {
		return
	}
	h.mu.RLock()
	var last []*sampleFrames
	for nodeID, ns := range h.nodes[c.orgID] {
		if ns.last != nil && c.subscribed(nodeID) {
			last = append(last, ns.last)
		}
	}
	h.mu.RUnlock()
	for _, f := range last {
		h.enqueue(c, "sample", f.legacy())
	}
}

// SetView replaces the lease of the client with the view of the frame. An
// empty view clears it. The hub then sends the last summary of each host
// that the old lease did not cover, and the last detail when the detail
// host or tab changed. A renewal of the same view sends nothing.
func (h *Hub) SetView(c *Client, m wire.ClientMessage) error {
	if !c.lease {
		return ErrLegacyClient
	}
	now := h.now()
	next := newLease(m, now)

	// The read lock keeps PublishSample from storing a sample between the
	// replay and the swap, so a replay never arrives after a newer frame.
	h.mu.RLock()
	defer h.mu.RUnlock()
	old := c.swapView(next)
	if !next.active(now) {
		return nil
	}
	if !old.active(now) {
		old = nil
	}
	byNode := h.nodes[c.orgID]

	send := func(host uuid.UUID) {
		if ns := byNode[host]; ns != nil && ns.last != nil {
			h.enqueue(c, "summary", ns.last.summaryFrame(true))
		}
	}
	for host := range next.hosts {
		if !old.covers(host, now) {
			send(host)
		}
	}
	d := next.detail
	if d == nil {
		return nil
	}
	if _, listed := next.hosts[d.Host]; !listed && !old.covers(d.Host, now) {
		send(d.Host)
	}
	if old != nil && old.detail != nil && *old.detail == *d {
		return nil
	}
	if ns := byNode[d.Host]; ns != nil {
		f := ns.last
		if d.Tab == wire.DetailPorts {
			f = ns.lastPorts
		}
		if f != nil {
			if data, ok := f.detailFrame(d.Tab); ok {
				h.enqueue(c, "detail", data)
			}
		}
	}
	return nil
}

// Covered reports whether an active lease of a client of the org covers
// the node. Expired leases do not count.
func (h *Hub) Covered(orgID, nodeID uuid.UUID) bool {
	now := h.now()
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients[orgID] {
		if c.liveView(now).covers(nodeID, now) {
			return true
		}
	}
	return false
}

func (h *Hub) now() time.Time { return h.opts.Now() }

// Run pumps messages to the given client until the context closes or
// the websocket errors out.
func (c *Client) Run(ctx context.Context) error {
	writeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Writer goroutine.
	go func() {
		for {
			select {
			case <-writeCtx.Done():
				return
			case data, ok := <-c.send:
				if !ok {
					return
				}
				wctx, wcancel := context.WithTimeout(writeCtx, 10*time.Second)
				err := c.conn.Write(wctx, websocket.MessageText, data)
				wcancel()
				if err != nil {
					_ = c.conn.Close(websocket.StatusInternalError, "write failed")
					return
				}
			}
		}
	}()

	// Reader loop: subscribe (legacy), view (lease) and ping frames.
	for {
		var in wire.ClientMessage
		if err := wsjson.Read(ctx, c.conn, &in); err != nil {
			return err
		}
		switch in.Type {
		case "subscribe":
			if !c.lease {
				c.setSubset(in.NodeIDs)
			}
		case "view":
			if err := c.hub.SetView(c, in); err != nil {
				c.log.Debug("hub: view ignored", "client", c.id, "err", err)
			}
		case "ping":
			_ = wsjson.Write(ctx, c.conn, wire.WSMessage{Type: "pong"})
		}
	}
}

// swapView sets the lease and returns the old one. It keeps the gauge of
// the leases.
func (c *Client) swapView(l *lease) *lease {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.view
	c.view = l
	switch {
	case old == nil && l != nil:
		streamLeasesActive.Inc()
	case old != nil && l == nil:
		streamLeasesActive.Dec()
	}
	return old
}

// liveView returns the lease, and drops it when it has expired.
func (c *Client) liveView(now time.Time) *lease {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.view != nil && !c.view.active(now) {
		c.view = nil
		streamLeasesActive.Dec()
	}
	return c.view
}

func (c *Client) setSubset(ids []uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ids) == 0 {
		c.subset = nil
		return
	}
	c.subset = make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		c.subset[id] = struct{}{}
	}
}

func (c *Client) subscribed(id uuid.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subset == nil {
		return true
	}
	_, ok := c.subset[id]
	return ok
}
