package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/query"
	"github.com/towertail/towertail/server/wire"
)

func TestSearchFetch(t *testing.T) {
	f := newFake()
	web := f.nodes[0]
	cs := connectOpts(t, f, tokenA(), func(o *Options) { o.AppURL = "https://app.example.com/" })

	out := decode[SearchOut](t, call(t, cs, "search", map[string]any{"query": "WEB"}))
	if len(out.Results) < 2 {
		t.Fatalf("results = %+v", out.Results)
	}
	host, alert := out.Results[0], out.Results[1]
	if host.ID != "host:"+web.ID.String() || host.Title != "web-1" || host.URL != "https://app.example.com/hosts/"+web.ID.String() {
		t.Errorf("host = %+v", host)
	}
	if !strings.HasPrefix(alert.ID, "alert:") || !strings.Contains(alert.Title, "web-1 cpu critical") || alert.URL != "https://app.example.com/alerts" {
		t.Errorf("alert = %+v", alert)
	}
	if f.gotAlertQ.Q != "WEB" || f.gotAlertQ.State != "all" || f.gotAlertQ.OrgID != orgA {
		t.Errorf("alert query = %+v", f.gotAlertQ)
	}

	doc := decode[FetchOut](t, call(t, cs, "fetch", map[string]any{"id": host.ID}))
	var h GetHostOut
	if err := json.Unmarshal([]byte(doc.Text), &h); err != nil {
		t.Fatal(err)
	}
	if doc.ID != host.ID || doc.Title != "web-1" || doc.Metadata.Kind != "host" || doc.URL != host.URL || h.Name != "web-1" {
		t.Errorf("host doc = %+v", doc)
	}
	f.alertDetail = wire.AlertDetail{Alert: wire.Alert{ID: uuid.MustParse(strings.TrimPrefix(alert.ID, "alert:")), NodeID: web.ID, NodeName: "web-1", Metric: "cpu", Tint: wire.TintCritical, State: "open"}}
	doc = decode[FetchOut](t, call(t, cs, "fetch", map[string]any{"id": alert.ID}))
	if doc.Metadata.Kind != "alert" || !strings.Contains(doc.Text, strings.TrimPrefix(alert.ID, "alert:")) {
		t.Errorf("alert doc = %+v", doc)
	}
}

func TestSearchReference(t *testing.T) {
	f := newFake()
	cs := connect(t, f, tokenA())
	out := decode[SearchOut](t, call(t, cs, "search", map[string]any{"query": "long process"}))
	var ids []string
	for _, r := range out.Results {
		ids = append(ids, r.ID)
		if r.URL != "" {
			t.Errorf("url without app url: %+v", r)
		}
	}
	if !strings.Contains(strings.Join(ids, " "), "reference:long_process") {
		t.Fatalf("ids = %v", ids)
	}
	doc := decode[FetchOut](t, call(t, cs, "fetch", map[string]any{"id": "reference:long_process"}))
	if doc.Title != "Reference: long_process" || doc.Metadata.Kind != "reference" || !strings.Contains(doc.Text, `"topic":"long_process"`) {
		t.Errorf("doc = %+v", doc)
	}
}

func TestSearchTagAndNoHistory(t *testing.T) {
	f := newFake()
	f.listErr = query.ErrNoHistory
	cs := connect(t, f, tokenA())
	out := decode[SearchOut](t, call(t, cs, "search", map[string]any{"query": "web"}))
	if len(out.Results) == 0 || out.Results[0].ID != "host:"+f.nodes[0].ID.String() {
		t.Fatalf("results = %+v", out.Results)
	}
	out = decode[SearchOut](t, call(t, cs, "search", map[string]any{"query": "zzz-nothing"}))
	if out.Results == nil || len(out.Results) != 0 {
		t.Fatalf("results = %#v, want an empty list", out.Results)
	}
	f.alertErr = clickhouse.ErrNoAlert
	if res := call(t, cs, "fetch", map[string]any{"id": "alert:" + uuid.NewString()}); !res.IsError {
		t.Fatalf("unknown alert must fail: %s", text(res))
	}
}
