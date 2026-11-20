package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	t     *testing.T
	h     *Hub
	clock *fakeClock
	org   uuid.UUID
}

func newEnv(t *testing.T, legacy bool) *env {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(log, Options{LegacySamples: legacy, Now: clock.now})
	return &env{t: t, h: h, clock: clock, org: uuid.New()}
}

func (e *env) client(lease bool) *Client {
	return e.h.Register(e.org, nil, ClientOptions{Lease: lease})
}

func sampleJSON(cpu float64, sampler string, ports bool) string {
	p := ""
	if ports {
		p = `,"ports":{"root":true,"collected_ts":"t","max_conn":1,"total":0,"items":[]}`
	}
	return fmt.Sprintf(`{"v":1,"ts":"2026-11-20T12:00:00.000Z","host":{"name":"h","os":"linux","arch":"amd64","uptime_s":1,"sampler":%q},`+
		`"cpu":{"pct":%v,"load_1":1,"load_5":0,"load_15":0,"cores":4},"mem":{"used":1,"total":4},"swap":{"used":0,"total":0},`+
		`"procs":{"root":true,"top_n":1,"total":9,"visible":9,"items":[{"pid":1,"name":"init","cpu_pct":1,"rss":2}]}%s,"errors":[]}`, sampler, cpu, p)
}

func (e *env) publish(node uuid.UUID, cpu float64, sampler string, ports bool) {
	e.t.Helper()
	raw := []byte(sampleJSON(cpu, sampler, ports))
	var s wire.Sample
	if err := json.Unmarshal(raw, &s); err != nil {
		e.t.Fatal(err)
	}
	e.h.PublishSample(e.org, node, s, raw)
}

func view(ttl int, hosts ...uuid.UUID) wire.ClientMessage {
	return wire.ClientMessage{Type: "view", Hosts: hosts, TTL: ttl}
}

func viewDetail(node uuid.UUID, tab wire.DetailTab, hosts ...uuid.UUID) wire.ClientMessage {
	return wire.ClientMessage{Type: "view", Hosts: hosts, Detail: &wire.ViewDetail{Host: node, Tab: tab}}
}

// drain reads the frames that wait on the client.
func drain(c *Client) []wire.WSMessage {
	var out []wire.WSMessage
	for {
		select {
		case data := <-c.send:
			var m wire.WSMessage
			if err := json.Unmarshal(data, &m); err != nil {
				panic(err)
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

func types(msgs []wire.WSMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Type
	}
	return out
}

func (e *env) wantTypes(c *Client, want ...string) []wire.WSMessage {
	e.t.Helper()
	got := drain(c)
	if fmt.Sprint(types(got)) != fmt.Sprint(want) {
		e.t.Fatalf("frames %v, want %v", types(got), want)
	}
	return got
}

func (e *env) mustView(c *Client, m wire.ClientMessage) {
	e.t.Helper()
	if err := e.h.SetView(c, m); err != nil {
		e.t.Fatal(err)
	}
}

func TestPublishDiskScanOnlyToClientsThatSeeTheNode(t *testing.T) {
	h := New(slog.Default(), Options{})
	org, other := uuid.New(), uuid.New()
	node, node2 := uuid.New(), uuid.New()

	sees := h.Register(org, nil, ClientOptions{})
	sees.setSubset([]uuid.UUID{node})
	all := h.Register(org, nil, ClientOptions{})
	lease := h.Register(org, nil, ClientOptions{Lease: true})
	notSub := h.Register(org, nil, ClientOptions{})
	notSub.setSubset([]uuid.UUID{node2})
	otherOrg := h.Register(other, nil, ClientOptions{})

	h.PublishDiskScan(org, wire.DiskScanJob{ID: uuid.New(), NodeID: node, State: "running"})

	for name, c := range map[string]*Client{"subscribed": sees, "unfiltered": all, "lease": lease} {
		select {
		case data := <-c.send:
			var msg wire.WSMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type != "disk_scan_updated" || msg.NodeID == nil || *msg.NodeID != node || msg.Job == nil || msg.Job.State != "running" {
				t.Errorf("%s: bad frame %s", name, data)
			}
		default:
			t.Errorf("%s: no frame", name)
		}
	}
	for name, c := range map[string]*Client{"other node": notSub, "other org": otherOrg} {
		select {
		case data := <-c.send:
			t.Errorf("%s: unexpected frame %s", name, data)
		default:
		}
	}
}

func TestLegacyClientGetsFullSample(t *testing.T) {
	e := newEnv(t, true)
	c, node := e.client(false), uuid.New()
	e.publish(node, 5, "s1", false)
	got := e.wantTypes(c, "sample")
	var s wire.Sample
	if err := json.Unmarshal(got[0].Sample, &s); err != nil || s.CPU.Pct != 5 || *got[0].NodeID != node {
		t.Fatalf("frame: %+v %v", got[0], err)
	}
	if string(got[0].Sample) != sampleJSON(5, "s1", false) {
		t.Errorf("sample bytes differ: %s", got[0].Sample)
	}

	late := e.client(false)
	e.h.ReplayFor(late)
	e.wantTypes(late, "sample")
}

func TestLegacyClientSubsetStillFilters(t *testing.T) {
	e := newEnv(t, true)
	c, a, b := e.client(false), uuid.New(), uuid.New()
	c.setSubset([]uuid.UUID{a})
	e.publish(a, 1, "s", false)
	e.publish(b, 1, "s", false)
	got := e.wantTypes(c, "sample")
	if *got[0].NodeID != a {
		t.Errorf("node %s", got[0].NodeID)
	}
}

func TestLegacyOffSendsEventsOnly(t *testing.T) {
	e := newEnv(t, false)
	node := uuid.New()
	e.publish(node, 1, "s", false)
	c := e.client(false)
	e.h.ReplayFor(c)
	e.wantTypes(c)
	e.publish(node, 2, "s", false)
	e.wantTypes(c)
	e.h.Publish(e.org, wire.Event{ID: uuid.New(), Kind: "threshold_crossed", NodeID: node})
	e.wantTypes(c, "event")
}

func TestLeaseClientWithoutViewGetsEventsOnly(t *testing.T) {
	e := newEnv(t, true)
	c, node := e.client(true), uuid.New()
	e.h.ReplayFor(c)
	if hello := e.wantTypes(c, "hello")[0].Hello; hello == nil || !hello.Lease || hello.RenewS != 30 || hello.MaxTTLS != 120 {
		t.Errorf("hello: %+v", hello)
	}
	e.publish(node, 1, "s", false)
	e.wantTypes(c)
	e.h.Publish(e.org, wire.Event{ID: uuid.New(), Kind: "threshold_crossed", NodeID: node})
	e.h.BroadcastOrg(e.org, wire.WSMessage{Type: "node_updated"})
	e.wantTypes(c, "event", "node_updated")
}

func TestLeaseClientGetsSummaryForLeasedHostsOnly(t *testing.T) {
	e := newEnv(t, true)
	c, a, b := e.client(true), uuid.New(), uuid.New()
	e.mustView(c, view(60, a))
	e.publish(a, 7, "s", false)
	e.publish(b, 8, "s", false)
	got := e.wantTypes(c, "summary")
	if *got[0].NodeID != a || got[0].Summary.CPU != 7 || got[0].Sample != nil {
		t.Errorf("frame: %+v", got[0])
	}
}

func TestDetailFrameOnlyForDetailHostAndTab(t *testing.T) {
	e := newEnv(t, true)
	c, a, b := e.client(true), uuid.New(), uuid.New()
	e.mustView(c, viewDetail(a, wire.DetailProcesses, b))
	e.publish(a, 1, "s", true)
	e.publish(b, 1, "s", true)
	got := e.wantTypes(c, "summary", "detail", "summary")
	if got[1].Tab != wire.DetailProcesses || *got[1].NodeID != a {
		t.Errorf("detail frame: %+v", got[1])
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(got[1].Detail, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["procs"]; !ok || len(keys) != 2 {
		t.Errorf("detail keys: %v", keys)
	}

	// A sample without ports gives no detail frame for the ports tab.
	e.mustView(c, viewDetail(a, wire.DetailPorts, b))
	drain(c)
	e.publish(a, 1, "s", false)
	e.wantTypes(c, "summary")
	e.publish(a, 1, "s", true)
	e.wantTypes(c, "summary", "detail")
}

func TestDetailHostCountsAsLeased(t *testing.T) {
	e := newEnv(t, true)
	c, a, b := e.client(true), uuid.New(), uuid.New()
	e.mustView(c, viewDetail(a, wire.DetailPorts, b))
	e.publish(a, 1, "s", false)
	e.wantTypes(c, "summary")
}

func TestLeaseExpires(t *testing.T) {
	e := newEnv(t, true)
	c, a := e.client(true), uuid.New()
	e.mustView(c, view(30, a))
	e.clock.add(29 * time.Second)
	if !e.h.Covered(e.org, a) {
		t.Fatal("not covered before the end")
	}
	e.clock.add(2 * time.Second)
	if e.h.Covered(e.org, a) {
		t.Error("covered after the ttl")
	}
	e.publish(a, 1, "s", false)
	e.wantTypes(c)
}

func TestEmptyViewClearsLease(t *testing.T) {
	e := newEnv(t, true)
	c, a := e.client(true), uuid.New()
	e.mustView(c, view(60, a))
	e.publish(a, 1, "s", false)
	e.wantTypes(c, "summary")
	e.mustView(c, view(60))
	e.publish(a, 1, "s", false)
	e.wantTypes(c)
	if e.h.Covered(e.org, a) {
		t.Error("covered after an empty view")
	}
	e.mustView(c, wire.ClientMessage{Type: "view", Detail: &wire.ViewDetail{Host: a, Tab: "bogus"}})
	e.publish(a, 1, "s", false)
	e.wantTypes(c)
}

func TestSetViewReplaysNewHostsOnly(t *testing.T) {
	e := newEnv(t, true)
	c, a, b := e.client(true), uuid.New(), uuid.New()
	e.publish(a, 1, "s", false)
	e.publish(b, 2, "s", false)

	e.mustView(c, view(60, a))
	got := e.wantTypes(c, "summary")
	if *got[0].NodeID != a || got[0].Summary.Host == nil || got[0].Summary.Host.Sampler != "s" {
		t.Errorf("replay: %+v", got[0])
	}
	e.mustView(c, view(60, a))
	e.wantTypes(c)
	e.mustView(c, view(60, a, b))
	got = e.wantTypes(c, "summary")
	if *got[0].NodeID != b {
		t.Errorf("replay of %s", got[0].NodeID)
	}
}

func TestSetViewReplaysDetailOnChange(t *testing.T) {
	e := newEnv(t, true)
	c, a := e.client(true), uuid.New()
	e.publish(a, 1, "s", false)
	e.mustView(c, viewDetail(a, wire.DetailProcesses))
	e.wantTypes(c, "summary", "detail")
	e.mustView(c, viewDetail(a, wire.DetailProcesses))
	e.wantTypes(c)
	e.mustView(c, viewDetail(a, wire.DetailDisks))
	e.wantTypes(c, "detail")
}

func TestSetViewReplaysLastPorts(t *testing.T) {
	e := newEnv(t, true)
	c, a := e.client(true), uuid.New()
	e.publish(a, 1, "s", true)
	e.publish(a, 2, "s", false)
	e.mustView(c, viewDetail(a, wire.DetailPorts))
	got := e.wantTypes(c, "summary", "detail")
	if got[0].Summary.CPU != 2 || got[1].Tab != wire.DetailPorts {
		t.Errorf("frames: %+v", got)
	}
}

func TestSetViewOnLegacyClient(t *testing.T) {
	e := newEnv(t, true)
	if err := e.h.SetView(e.client(false), view(60, uuid.New())); err != ErrLegacyClient {
		t.Errorf("err = %v", err)
	}
}

func TestCoveredUnionAndUnregister(t *testing.T) {
	e := newEnv(t, true)
	c1, c2, a := e.client(true), e.client(true), uuid.New()
	e.mustView(c1, view(60, a))
	e.mustView(c2, view(60, a))
	e.h.Unregister(c1)
	if !e.h.Covered(e.org, a) {
		t.Error("not covered with one client left")
	}
	e.h.Unregister(c2)
	if e.h.Covered(e.org, a) {
		t.Error("covered with no client")
	}
	if e.h.Covered(uuid.New(), a) {
		t.Error("covered in another org")
	}
}

func TestClampTTL(t *testing.T) {
	for in, want := range map[int]time.Duration{0: 60 * time.Second, 1: 10 * time.Second, 45: 45 * time.Second, 999: 120 * time.Second, -5: 10 * time.Second} {
		if got := clampTTL(in); got != want {
			t.Errorf("clampTTL(%d) = %v, want %v", in, got, want)
		}
	}
}

func TestViewHostsAreCut(t *testing.T) {
	hosts := make([]uuid.UUID, maxViewHosts+10)
	for i := range hosts {
		hosts[i] = uuid.New()
	}
	l := newLease(view(60, hosts...), time.Now())
	if len(l.hosts) != maxViewHosts {
		t.Errorf("hosts = %d", len(l.hosts))
	}
}

func TestFactsOnlyOnChange(t *testing.T) {
	e := newEnv(t, true)
	c, a := e.client(true), uuid.New()
	e.mustView(c, view(60, a))

	e.publish(a, 1, "s1", false)
	if got := e.wantTypes(c, "summary"); got[0].Summary.Host == nil {
		t.Error("first frame has no host")
	}
	e.publish(a, 1, "s1", false)
	if got := e.wantTypes(c, "summary"); got[0].Summary.Host != nil {
		t.Error("same facts, host again")
	}
	e.publish(a, 1, "s2", false)
	if got := e.wantTypes(c, "summary"); got[0].Summary.Host == nil || got[0].Summary.Host.Sampler != "s2" {
		t.Error("new sampler, no host")
	}
}

func TestHealthOnlyOnChange(t *testing.T) {
	e := newEnv(t, true)
	c, a := e.client(true), uuid.New()
	e.mustView(c, view(60, a))
	publish := func(zombies int) {
		raw := []byte(fmt.Sprintf(`{"v":1,"ts":"t","host":{},"cpu":{},"mem":{},"swap":{},"health":{"procs":3,"zombies":%d},"errors":[]}`, zombies))
		var s wire.Sample
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		e.h.PublishSample(e.org, a, s, raw)
	}
	has := func(want bool) {
		t.Helper()
		if got := e.wantTypes(c, "summary"); (got[0].Summary.Health != nil) != want {
			t.Errorf("health = %+v, want present %v", got[0].Summary.Health, want)
		}
	}
	publish(0)
	has(true)
	publish(0)
	has(false)
	publish(2)
	has(true)
}

func TestSlowConsumerKeepsOthers(t *testing.T) {
	e := newEnv(t, true)
	slow, fast, a := e.client(true), e.client(true), uuid.New()
	e.mustView(slow, view(60, a))
	e.mustView(fast, view(60, a))
	for i := 0; i < cap(slow.send)+5; i++ {
		e.publish(a, 1, "s", false)
		drain(fast)
	}
	e.publish(a, 1, "s", false)
	e.wantTypes(fast, "summary")
}
