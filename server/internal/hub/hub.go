package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/towertail/server/internal/wire"
)

// Hub fans out samples and events to connected websocket clients.
// Connections are grouped by org. Slow consumers are dropped rather
// than being allowed to backpressure the fast path.
type Hub struct {
	log *slog.Logger

	mu      sync.RWMutex
	clients map[uuid.UUID]map[*Client]struct{}

	lastSampleMu sync.RWMutex
	lastSample   map[uuid.UUID]map[uuid.UUID]json.RawMessage // orgID → nodeID → raw
}

type Client struct {
	id     uuid.UUID
	orgID  uuid.UUID
	conn   *websocket.Conn
	send   chan []byte
	log    *slog.Logger
	subset map[uuid.UUID]struct{}
	mu     sync.Mutex
}

func New(log *slog.Logger) *Hub {
	return &Hub{
		log:        log,
		clients:    map[uuid.UUID]map[*Client]struct{}{},
		lastSample: map[uuid.UUID]map[uuid.UUID]json.RawMessage{},
	}
}

// Register installs a new client for the given org and returns a handle
// the caller uses to feed inbound frames.
func (h *Hub) Register(orgID uuid.UUID, conn *websocket.Conn) *Client {
	c := &Client{
		id:    uuid.New(),
		orgID: orgID,
		conn:  conn,
		send:  make(chan []byte, 128),
		log:   h.log,
	}
	h.mu.Lock()
	if _, ok := h.clients[orgID]; !ok {
		h.clients[orgID] = map[*Client]struct{}{}
	}
	h.clients[orgID][c] = struct{}{}
	h.mu.Unlock()
	return c
}

// Unregister removes the client from its org set and closes the send
// channel.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if set, ok := h.clients[c.orgID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, c.orgID)
		}
	}
	h.mu.Unlock()
	close(c.send)
}

// PublishSample fans a sample raw JSON out to every client subscribed
// to the node's org. Also caches the most recent per-node sample so
// late joiners get an immediate paint.
func (h *Hub) PublishSample(orgID, nodeID uuid.UUID, raw json.RawMessage) {
	h.lastSampleMu.Lock()
	if _, ok := h.lastSample[orgID]; !ok {
		h.lastSample[orgID] = map[uuid.UUID]json.RawMessage{}
	}
	h.lastSample[orgID][nodeID] = raw
	h.lastSampleMu.Unlock()

	msg := wire.WSMessage{Type: "sample", NodeID: &nodeID, Sample: raw}
	data, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("hub: marshal sample", "err", err)
		return
	}
	h.broadcast(orgID, data, &nodeID)
}

// Publish (EventSink satisfied) pushes an event to all clients in the org.
func (h *Hub) Publish(orgID uuid.UUID, e wire.Event) {
	msg := wire.WSMessage{Type: "event", Event: &e}
	data, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("hub: marshal event", "err", err)
		return
	}
	h.broadcast(orgID, data, &e.NodeID)
}

// BroadcastOrg sends a pre-serialised frame to every client in the
// given org. Used by the API layer for node_updated / settings_updated.
func (h *Hub) BroadcastOrg(orgID uuid.UUID, msg wire.WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("hub: marshal", "err", err)
		return
	}
	h.broadcast(orgID, data, nil)
}

func (h *Hub) broadcast(orgID uuid.UUID, data []byte, nodeID *uuid.UUID) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients[orgID]))
	for c := range h.clients[orgID] {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		if nodeID != nil && c.hasFilter() && !c.subscribed(*nodeID) {
			continue
		}
		select {
		case c.send <- data:
		default:
			h.log.Warn("hub: slow consumer dropped", "client", c.id)
			_ = c.conn.Close(websocket.StatusPolicyViolation, "slow consumer")
		}
	}
}

// ReplayFor pushes the last known sample for each node to the given
// client so a reconnect paints immediately.
func (h *Hub) ReplayFor(c *Client) {
	h.lastSampleMu.RLock()
	byNode := map[uuid.UUID]json.RawMessage{}
	for k, v := range h.lastSample[c.orgID] {
		byNode[k] = v
	}
	h.lastSampleMu.RUnlock()

	for nodeID, raw := range byNode {
		if c.hasFilter() && !c.subscribed(nodeID) {
			continue
		}
		nid := nodeID
		msg := wire.WSMessage{Type: "sample", NodeID: &nid, Sample: raw}
		if data, err := json.Marshal(msg); err == nil {
			select {
			case c.send <- data:
			default:
			}
		}
	}
}

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

	// Reader loop — handles subscribe/ping frames.
	for {
		var in wire.SubscribeMessage
		if err := wsjson.Read(ctx, c.conn, &in); err != nil {
			return err
		}
		switch in.Type {
		case "subscribe":
			c.setSubset(in.NodeIDs)
		case "ping":
			_ = wsjson.Write(ctx, c.conn, wire.WSMessage{Type: "pong"})
		}
	}
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

func (c *Client) hasFilter() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subset != nil
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
