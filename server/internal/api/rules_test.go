package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

// stream opens the event stream as a viewer.
func (e *alertEnv) stream(t *testing.T) (context.Context, *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, strings.Replace(e.ts.URL, "http", "ws", 1)+"/v1/stream", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + e.tokens[store.RoleViewer]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ctx, ws
}

// frame reads the stream until a frame of the type comes.
func frame(t *testing.T, ctx context.Context, ws *websocket.Conn, typ string) wire.WSMessage {
	t.Helper()
	for {
		var m wire.WSMessage
		if err := json.Unmarshal(readRaw(t, ctx, ws), &m); err != nil {
			t.Fatal(err)
		}
		if m.Type == typ {
			return m
		}
	}
}

func (e *alertEnv) rulesOf(id uuid.UUID) string { return "/v1/nodes/" + id.String() + "/rules" }

func TestGetRulesGivesEffectiveAndSources(t *testing.T) {
	e := newAlertEnv(t)
	body := `{"thresholds":{"mem":{"warn":0.85,"critical":0.95}}}`
	if code, b := e.do(store.RoleAdmin, "PUT", e.rulesOf(e.node), body); code != http.StatusOK {
		t.Fatalf("put: %d %s", code, b)
	}
	code, b := e.do(store.RoleViewer, "GET", "/v1/rules", "")
	if code != http.StatusOK {
		t.Fatalf("get: %d %s", code, b)
	}
	var v struct {
		Org   store.OrgRules `json:"org"`
		Hosts []struct {
			NodeID    uuid.UUID            `json:"nodeId"`
			Host      store.HostRules      `json:"host"`
			Effective store.EffectiveRules `json:"effective"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Hosts) != 1 || v.Hosts[0].NodeID != e.node {
		t.Fatalf("hosts = %+v", v.Hosts)
	}
	eff := v.Hosts[0].Effective
	if eff.Thresholds.MemWarn != 0.85 || eff.Thresholds.CPUWarn != v.Org.Thresholds.CPUWarn {
		t.Fatalf("effective = %+v", eff.Thresholds)
	}
	if eff.Sources.Thresholds[store.MetricMem] != store.SourceHost || eff.Sources.Thresholds[store.MetricCPU] != store.SourceOrg {
		t.Fatalf("sources = %+v", eff.Sources.Thresholds)
	}

	code, b = e.do(store.RoleViewer, "GET", e.rulesOf(e.node), "")
	if code != http.StatusOK {
		t.Fatalf("get node rules: %d %s", code, b)
	}
	var one struct {
		NodeID    uuid.UUID            `json:"nodeId"`
		Host      store.HostRules      `json:"host"`
		Effective store.EffectiveRules `json:"effective"`
	}
	if err := json.Unmarshal(b, &one); err != nil || one.NodeID != e.node || one.Effective.Thresholds.MemWarn != 0.85 || one.Host.Thresholds.Mem == nil {
		t.Fatalf("node rules = %s, %v", b, err)
	}
	if code, _ := e.do(store.RoleViewer, "GET", e.rulesOf(uuid.New()), ""); code != http.StatusNotFound {
		t.Fatalf("unknown host: %d", code)
	}
	if code, _ := e.do(store.RoleViewer, "GET", "/v1/nodes/x/rules", ""); code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", code)
	}
}

func TestPutNodeRulesKeepsOtherNodeFields(t *testing.T) {
	e := newAlertEnv(t)
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	n := e.nodeNow()
	n.HideProcFolders = true
	n.SnoozedUntil = &until
	if err := e.store.PutNode(context.Background(), store.ZeroOrg, n); err != nil {
		t.Fatal(err)
	}
	ctx, ws := e.stream(t)

	body := `{"thresholds":{"cpu":{"warn":0.5,"critical":0.6}},"alerts":{"cpu":{"sustainSeconds":60,"notify":"all"}},"muted":["disk"],"origin":{"client":"mac","device":"mbp"}}`
	if code, b := e.do(store.RoleAdmin, "PUT", e.rulesOf(e.node), body); code != http.StatusOK {
		t.Fatalf("put: %d %s", code, b)
	}
	got := e.nodeNow()
	if got.DisplayName != "web1" || !got.HideProcFolders || got.SnoozedUntil == nil || !got.SnoozedUntil.Equal(until) {
		t.Fatalf("other fields changed: %+v", got)
	}
	if got.ThresholdOverrides == nil || got.ThresholdOverrides.CPU.Warn != 0.5 || got.AlertOverrides.CPU.SustainSeconds != 60 || !got.MutedSignals.Has(store.MetricDisk) {
		t.Fatalf("rules = %+v", got)
	}
	if o := got.RulesOrigin; o == nil || o.Client != "mac" || o.Device != "mbp" || o.UserID == uuid.Nil || time.Since(o.At) > time.Minute {
		t.Fatalf("origin = %+v", o)
	}
	m := frame(t, ctx, ws, "node_updated")
	var sent store.Node
	if err := json.Unmarshal(m.Node, &sent); err != nil || sent.ID != e.node || sent.ThresholdOverrides == nil {
		t.Fatalf("frame = %s, %v", m.Node, err)
	}

	// A null or missing field clears that override.
	if code, b := e.do(store.RoleAdmin, "PUT", e.rulesOf(e.node), `{"thresholds":null}`); code != http.StatusOK {
		t.Fatalf("put: %d %s", code, b)
	}
	got = e.nodeNow()
	if got.ThresholdOverrides != nil || got.AlertOverrides != nil || len(got.MutedSignals) != 0 || got.RulesOrigin.Client != "api" {
		t.Fatalf("not cleared: %+v", got)
	}
}

func TestPutNodeRulesRejectsBadPair(t *testing.T) {
	e := newAlertEnv(t)
	for _, body := range []string{
		`{"thresholds":{"cpu":{"warn":0.9,"critical":0.5}}}`,
		`{"alerts":{"cpu":{"sustainSeconds":-1,"notify":"all"}}}`,
		`{"alerts":{"cpu":{"sustainSeconds":10,"notify":"loud"}}}`,
	} {
		if code, b := e.do(store.RoleAdmin, "PUT", e.rulesOf(e.node), body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, code, b)
		}
	}
	if n := e.nodeNow(); n.ThresholdOverrides != nil || n.AlertOverrides != nil || n.RulesOrigin != nil {
		t.Fatalf("a bad body wrote: %+v", n)
	}
	if code, _ := e.do(store.RoleAdmin, "PUT", e.rulesOf(uuid.New()), `{}`); code != http.StatusNotFound {
		t.Fatalf("unknown host: %d", code)
	}
}

func TestPutMuteRoles(t *testing.T) {
	e := newAlertEnv(t)
	host := `{"metric":"health","nodeId":"` + e.node.String() + `","muted":true}`
	org := `{"metric":"inodes","muted":true}`
	for _, c := range []struct {
		role store.Role
		body string
		want int
	}{
		{store.RoleViewer, host, http.StatusForbidden},
		{store.RoleMember, host, http.StatusBadRequest}, // health is not a mute name
		{store.RoleMember, `{"metric":"zombies","nodeId":"` + e.node.String() + `","muted":true}`, http.StatusOK},
		{store.RoleMember, org, http.StatusForbidden},
		{store.RoleAdmin, org, http.StatusOK},
	} {
		if code, b := e.do(c.role, "PUT", "/v1/mutes", c.body); code != c.want {
			t.Errorf("%s %s: %d, want %d: %s", c.role, c.body, code, c.want, b)
		}
	}
	if !e.nodeNow().MutedSignals.Has(store.MetricZombies) || !e.settings().Muted.Has(store.MetricInodes) {
		t.Fatal("mutes not stored")
	}
}

func TestImportRulesIsAllOrNothing(t *testing.T) {
	e := newAlertEnv(t)
	before := e.settings()
	body := `{"org":{"thresholds":{"cpuWarn":0.5,"cpuCritical":0.6}},"hosts":[
		{"nodeId":"` + e.node.String() + `","rules":{"muted":["disk"]}},
		{"nodeId":"` + uuid.NewString() + `","rules":{"thresholds":{"mem":{"warn":0.9,"critical":0.5}}}}]}`
	if code, b := e.do(store.RoleAdmin, "POST", "/v1/rules/import", body); code != http.StatusBadRequest {
		t.Fatalf("invalid host: %d %s", code, b)
	}
	unknown := `{"org":{"thresholds":{"cpuWarn":0.5,"cpuCritical":0.6}},"hosts":[
		{"nodeId":"` + e.node.String() + `","rules":{"muted":["disk"]}},
		{"nodeId":"` + uuid.NewString() + `","rules":{}}]}`
	if code, b := e.do(store.RoleAdmin, "POST", "/v1/rules/import", unknown); code != http.StatusNotFound {
		t.Fatalf("unknown host: %d %s", code, b)
	}
	if code, _ := e.do(store.RoleAdmin, "POST", "/v1/rules/import", `{"hosts":[]}`); code != http.StatusBadRequest {
		t.Fatalf("empty: %d", code)
	}
	if got := e.settings(); got.Thresholds.CPUWarn != before.Thresholds.CPUWarn || got.RulesOrigin != nil {
		t.Fatalf("org changed: %+v", got.Thresholds)
	}
	if n := e.nodeNow(); len(n.MutedSignals) != 0 || n.RulesOrigin != nil {
		t.Fatalf("host changed: %+v", n)
	}
	if code, b := e.do(store.RoleAdmin, "GET", "/v1/rules/backups", ""); code != http.StatusOK || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("backups: %d %s", code, b)
	}
}

func importOK(t *testing.T, e *alertEnv, body string) uuid.UUID {
	t.Helper()
	code, b := e.do(store.RoleAdmin, "POST", "/v1/rules/import", body)
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, b)
	}
	var r struct {
		BackupID uuid.UUID `json:"backupId"`
		Hosts    int       `json:"hosts"`
		Org      bool      `json:"org"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.BackupID == uuid.Nil || r.Hosts != 1 || !r.Org {
		t.Fatalf("result = %s, %v", b, err)
	}
	return r.BackupID
}

func TestImportRulesWritesBackupAndOrigin(t *testing.T) {
	e := newAlertEnv(t)
	ctx, ws := e.stream(t)
	before := e.settings()
	body := `{"client":"mac","device":"mbp","org":{"thresholds":{"cpuWarn":0.5,"cpuCritical":0.6},"muted":["zombies"]},
		"hosts":[{"nodeId":"` + e.node.String() + `","rules":{"thresholds":{"mem":{"warn":0.7,"critical":0.8}}}}]}`
	id := importOK(t, e, body)

	got := e.settings()
	if got.Thresholds.CPUWarn != 0.5 || !got.Muted.Has(store.MetricZombies) || got.RulesOrigin == nil || got.RulesOrigin.Client != "mac" || got.RulesOrigin.Device != "mbp" {
		t.Fatalf("settings = %+v", got)
	}
	n := e.nodeNow()
	if n.ThresholdOverrides == nil || n.ThresholdOverrides.Mem.Warn != 0.7 || n.RulesOrigin == nil || n.RulesOrigin.Client != "mac" || n.RulesOrigin.Device != "mbp" {
		t.Fatalf("node = %+v", n)
	}
	frame(t, ctx, ws, "settings_updated")
	frame(t, ctx, ws, "node_updated")

	code, b := e.do(store.RoleAdmin, "GET", "/v1/rules/backups", "")
	if code != http.StatusOK {
		t.Fatalf("backups: %d %s", code, b)
	}
	var list []store.RulesBackup
	if err := json.Unmarshal(b, &list); err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("backups = %s, %v", b, err)
	}
	if list[0].Org == nil || list[0].Org.Thresholds.CPUWarn != before.Thresholds.CPUWarn || list[0].Hosts[e.node].Thresholds != nil {
		t.Fatalf("backup content = %+v", list[0])
	}
}

func TestRestoreRulesBackup(t *testing.T) {
	e := newAlertEnv(t)
	before := e.settings()
	id := importOK(t, e, `{"org":{"thresholds":{"cpuWarn":0.5,"cpuCritical":0.6}},
		"hosts":[{"nodeId":"`+e.node.String()+`","rules":{"muted":["disk"]}}]}`)

	if code, _ := e.do(store.RoleMember, "POST", "/v1/rules/backups/"+id.String()+"/restore", ""); code != http.StatusForbidden {
		t.Fatalf("member: %d", code)
	}
	if code, _ := e.do(store.RoleAdmin, "POST", "/v1/rules/backups/"+uuid.NewString()+"/restore", ""); code != http.StatusNotFound {
		t.Fatalf("unknown backup: %d", code)
	}
	code, b := e.do(store.RoleAdmin, "POST", "/v1/rules/backups/"+id.String()+"/restore", `{"client":"web"}`)
	if code != http.StatusOK {
		t.Fatalf("restore: %d %s", code, b)
	}
	got := e.settings()
	if got.Thresholds.CPUWarn != before.Thresholds.CPUWarn || got.RulesOrigin == nil || got.RulesOrigin.Client != "web" {
		t.Fatalf("settings = %+v", got)
	}
	if n := e.nodeNow(); len(n.MutedSignals) != 0 {
		t.Fatalf("node = %+v", n)
	}
	// The restore made a backup of the rules that it replaced.
	_, b = e.do(store.RoleAdmin, "GET", "/v1/rules/backups", "")
	var list []store.RulesBackup
	if err := json.Unmarshal(b, &list); err != nil || len(list) != 2 || list[0].Org.Thresholds.CPUWarn != 0.5 {
		t.Fatalf("backups = %s, %v", b, err)
	}

	// A host that is gone is skipped.
	if code, b := e.do(store.RoleAdmin, "DELETE", "/v1/nodes/"+e.node.String(), ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, b)
	}
	if code, b := e.do(store.RoleAdmin, "POST", "/v1/rules/backups/"+id.String()+"/restore", ""); code != http.StatusOK {
		t.Fatalf("restore after delete: %d %s", code, b)
	}
}
