package mcp

import (
	"testing"
	"time"

	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

func TestAlertStatsTool(t *testing.T) {
	f := newFake()
	web := f.nodes[0]
	f.stats = &query.AlertStats{
		From: now.Add(-7 * 24 * time.Hour), To: now, Clamped: true, Truncated: true,
		Alerts: 4, Sends: 3, EmailSends: 2,
		ByHost: []query.CountRow{{Key: web.ID.String(), Name: "web-1", Alerts: 3, Critical: 1, Warn: 2, Sends: 3}},
		ByKind: []query.CountRow{{Key: "cpu", Name: "cpu", Alerts: 4, Warn: 4}},
		ByRule: []query.RuleRow{
			{Kind: "cpu", Source: store.SourceHost, NodeID: &web.ID, Host: "web-1", Warn: 0.9, Critical: 0.95, SustainSeconds: 300, Notify: store.NotifyCritical, Alerts: 2, Sends: 2},
			{Kind: "long", Source: store.SourceOrg, Warn: 50, SustainSeconds: 600, Notify: "all", Alerts: 1},
			{Kind: "nodata", Source: store.SourceOrg, Alerts: 1},
		},
		Flapping:  []query.FlapRow{{NodeID: web.ID, Host: "web-1", Metric: "disk", Alerts: 4, MedianDuration: 5 * time.Minute}},
		ByChannel: []query.ChannelRow{{Name: "Email ops", Kind: store.ChannelEmail, Sends: 2}, {Name: "Slack", Sends: 1, Failed: 1}},
		Bins:      []wire.AlertBin{{Start: now.Add(-time.Hour), End: now, Critical: 1, Warn: 2}},
	}
	cs := connect(t, f, tokenA())
	out := decode[AlertStatsOut](t, call(t, cs, "alert_stats", map[string]any{"host": "web-1", "kind": []string{"cpu"}, "top": 5}))

	req := f.gotStatsReq
	if req.OrgID != orgA || len(req.Hosts) != 1 || req.Hosts[0] != web.ID || len(req.Kinds) != 1 || req.Top != 5 ||
		!req.From.Equal(now.Add(-7*24*time.Hour)) || !req.To.Equal(now) {
		t.Fatalf("request = %+v", req)
	}
	if !out.Clamped || !out.Truncated || out.Alerts != 4 || out.Notifications != 3 || out.EmailSends != 2 || out.From != "2026-11-13T12:00:00Z" {
		t.Fatalf("totals = %+v", out)
	}
	if h := out.ByHost[0]; h.Name != "web-1" || h.Alerts != 3 || h.Critical != 1 || h.Notifications != 3 {
		t.Errorf("by host = %+v", out.ByHost)
	}
	if r := out.ByRule[0]; r.Source != "host" || r.HostID != web.ID.String() || r.Warn == nil || *r.Warn != 0.9 || r.Unit != "ratio 0 to 1" || r.Notify != "critical" {
		t.Errorf("host rule = %+v", r)
	}
	if r := out.ByRule[1]; r.CPUPct == nil || *r.CPUPct != 50 || r.Warn != nil || r.Host != "" {
		t.Errorf("long rule = %+v", r)
	}
	if r := out.ByRule[2]; r.Warn != nil || r.CPUPct != nil {
		t.Errorf("nodata rule = %+v", r)
	}
	if f := out.Flapping[0]; f.Metric != "disk" || f.MedianDurationSecond != 300 || f.HostID != web.ID.String() {
		t.Errorf("flapping = %+v", out.Flapping)
	}
	if len(out.ByChannel) != 2 || out.ByChannel[0].Kind != "email" || out.ByChannel[1].Kind != "" || out.ByChannel[1].Failed != 1 {
		t.Errorf("by channel = %+v", out.ByChannel)
	}
	if len(out.Bins) != 1 || out.Bins[0].Warn != 2 {
		t.Errorf("bins = %+v", out.Bins)
	}
}

func TestAlertStatsToolDefaults(t *testing.T) {
	f := newFake()
	cs := connect(t, f, tokenA())
	out := decode[AlertStatsOut](t, call(t, cs, "alert_stats", nil))
	if f.gotStatsReq.Hosts != nil || !f.gotStatsReq.From.Equal(now.Add(-7*24*time.Hour)) || f.gotStatsReq.Top != 0 {
		t.Fatalf("request = %+v", f.gotStatsReq)
	}
	if out.ByHost == nil || out.ByRule == nil || out.Flapping == nil || out.ByChannel == nil {
		t.Fatalf("empty lists must not be null: %+v", out)
	}
	f.statsErr = query.ErrNoHistory
	if res := call(t, cs, "alert_stats", nil); !res.IsError {
		t.Fatalf("want an error: %s", text(res))
	}
}
