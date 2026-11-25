package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/internal/alerter"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
)

func TestPutSettingsKeepsAbsentRuleKeys(t *testing.T) {
	e := newAlertEnv(t)
	stored := e.settings()
	stored.Alerts.CPU = store.AlertRule{SustainSeconds: 77, Notify: store.NotifyOff}
	stored.Thresholds.Health.PIDs = store.ThresholdPair{Warn: 0.5, Critical: 0.6}
	stored.Thresholds.ProcsWarn = 1234
	stored.Muted = store.MutedSignals{store.MetricZombies}
	stored.NotifyCooldownSeconds = 99
	if err := e.store.PutSettings(t.Context(), store.ZeroOrg, stored); err != nil {
		t.Fatal(err)
	}
	// The body of an old client: the six usage values only.
	body := `{"thresholds":{"cpuWarn":0.6,"cpuCritical":0.7,"memWarn":0.75,"memCritical":0.9,"diskWarn":0.85,"diskCritical":0.95},"notifyWarn":true}`
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/settings", body); code != http.StatusOK {
		t.Fatalf("put: %d %s", code, b)
	}
	got := e.settings()
	if got.Thresholds.CPUWarn != 0.6 || !got.NotifyWarn {
		t.Fatalf("body not applied: %+v", got)
	}
	if got.Alerts.CPU != stored.Alerts.CPU || got.Thresholds.Health.PIDs != stored.Thresholds.Health.PIDs ||
		got.Thresholds.ProcsWarn != 1234 || !got.Muted.Has(store.MetricZombies) || got.NotifyCooldownSeconds != 99 {
		t.Fatalf("absent keys lost: %+v", got)
	}
	// The change of a rule sets the origin. The client comes from the body.
	if o := got.RulesOrigin; o == nil || o.Client != "api" || o.UserID.String() == "" {
		t.Fatalf("origin = %+v", o)
	}
	body = `{"alerts":{"mem":{"sustainSeconds":30,"notify":"all"}},"rulesOrigin":{"client":"web","device":"firefox"}}`
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/settings", body); code != http.StatusOK {
		t.Fatalf("put: %d %s", code, b)
	}
	got = e.settings()
	if got.Alerts.Mem.SustainSeconds != 30 || got.Alerts.CPU != stored.Alerts.CPU || got.RulesOrigin.Client != "web" || got.RulesOrigin.Device != "firefox" {
		t.Fatalf("settings = %+v", got)
	}
	// A body with no rule change keeps the origin.
	at := got.RulesOrigin.At
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/settings", `{"notifyCritical":true,"rulesOrigin":{"client":"mac"}}`); code != http.StatusOK {
		t.Fatalf("put: %d %s", code, b)
	}
	if o := e.settings().RulesOrigin; o.Client != "web" || !o.At.Equal(at) {
		t.Fatalf("origin changed: %+v", o)
	}
}

func TestPutSettingsRejectsBadRules(t *testing.T) {
	e := newAlertEnv(t)
	before := e.settings()
	for _, body := range []string{
		`{"thresholds":{"cpuWarn":0.9,"cpuCritical":0.5}}`,
		`{"thresholds":{"health":{"pids":{"warn":0.9,"critical":0.5}}}}`,
		`{"alerts":{"tolerance":0.1}}`,
		`{"alerts":{"health":{"sustainSeconds":99999,"notify":"all"}}}`,
	} {
		if code, b := e.do(store.RoleAdmin, "PUT", "/v1/settings", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, code, b)
		}
	}
	if got := e.settings(); got.Thresholds != before.Thresholds || got.Alerts != before.Alerts {
		t.Fatalf("a bad body wrote: %+v", got)
	}
}

func TestPutSettingsDropsAlerterCache(t *testing.T) {
	e := newIngestEnv(t, config.Config{}, nil)
	n := make(chanNotifier, 4)
	e.srv.alerter = alerter.New(nil, nil, nil, &registeredStore{}, e.st, n)
	e.srv.actions = actions.NewService(e.st, nil, nil, e.srv.alerter, nil, nil)
	ctx := t.Context()
	s := store.DefaultServerSettings()
	s.Alerts.CPU = store.AlertRule{Notify: store.NotifyOff}
	if err := e.st.PutSettings(ctx, store.ZeroOrg, s); err != nil {
		t.Fatal(err)
	}
	_, tok := e.node(func(n *store.Node) { n.NotifyOnWarn, n.NotifyOnCritical = true, true })
	cpu := func(ts string, pct int) []byte {
		line := strings.Replace(sampleLine, "2026-04-23T10:00:00.000Z", ts, 1)
		return []byte(strings.Replace(line, `"pct":1,`, fmt.Sprintf(`"pct":%d,`, pct), 1))
	}
	wantCode(t, e.post(tok, cpu("2026-04-23T10:00:00.000Z", 10)), http.StatusAccepted)

	user := uuid.New()
	raw, _, err := e.issuer.Issue(ctx, store.ZeroOrg, store.TokenKindUser, "t", nil, &user)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/v1/settings", strings.NewReader(`{"alerts":{"cpu":{"sustainSeconds":0,"notify":"all"}}}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	wantCode(t, rec, http.StatusOK)

	wantCode(t, e.post(tok, cpu("2026-04-23T10:00:05.000Z", 95)), http.StatusAccepted)
	select {
	case a := <-n:
		if a.Metric != "cpu" || a.To != "critical" {
			t.Fatalf("alert = %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the engine kept the old notify level")
	}
}

// chanNotifier sends each alert on a channel.
type chanNotifier chan notify.Alert

func (chanNotifier) Name() string { return "chan" }

func (n chanNotifier) Notify(_ context.Context, a notify.Alert) error {
	n <- a
	return nil
}

func TestPutSettingsRejectsBadMCP(t *testing.T) {
	e := newAlertEnv(t)
	for _, body := range []string{`{"mcp":{"signInDays":3}}`, `{"mcp":{"aiChanges":"all"}}`} {
		if code, b := e.do(store.RoleAdmin, "PUT", "/v1/settings", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, code, b)
		}
	}
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/settings", `{"mcp":{"signInDays":90,"aiChanges":"alerts_and_rules"}}`); code != http.StatusOK {
		t.Fatalf("good body: %d %s", code, b)
	}
	got := e.settings().MCP
	if got.SignInDays != 90 || got.AIChanges != store.AIChangesAlertsAndRules || !got.APIKeys || !got.AnyClient {
		t.Errorf("mcp = %+v", got)
	}
}

func TestPutSettingsBackupOnlyOnRuleChange(t *testing.T) {
	e := newAlertEnv(t)
	put := func(body string, want int) {
		t.Helper()
		if code, b := e.do(store.RoleAdmin, "PUT", "/v1/settings", body); code != want {
			t.Fatalf("%s: %d %s, want %d", body, code, b, want)
		}
	}
	put(`{"sshPollingIntervalSeconds":45}`, http.StatusOK)
	put(`{"mcp":{"signInDays":90,"aiChanges":"alerts_and_rules"}}`, http.StatusOK)
	put(`{"thresholds":{"cpuWarn":0.75}}`, http.StatusOK) // the same value
	if n := len(e.backups(t)); n != 0 {
		t.Fatalf("%d backups after a change of no rule, want 0", n)
	}
	if e.settings().RulesOrigin != nil {
		t.Errorf("origin = %+v, want none", e.settings().RulesOrigin)
	}
	put(`{"thresholds":{"cpuWarn":0.8}}`, http.StatusOK)
	list := e.backups(t)
	if len(list) != 1 || list[0].Org == nil || list[0].Org.Thresholds.CPUWarn != 0.75 || len(list[0].Hosts) != 0 {
		t.Fatalf("backups = %+v", list)
	}
	if o := e.settings().RulesOrigin; o == nil || !o.At.Equal(list[0].At) {
		t.Errorf("origin = %+v, backup time %v", o, list[0].At)
	}
	put(`{"notifyCooldownSeconds":1800}`, http.StatusOK)
	if list = e.backups(t); len(list) != 2 || list[0].Org.NotifyCooldownSeconds != 600 {
		t.Fatalf("backups after a cooldown change = %+v", list)
	}
	put(`{"notifyCooldownSeconds":90000}`, http.StatusBadRequest)
	put(`{"mcp":{"aiChanges":"bogus"}}`, http.StatusBadRequest)
	if n := len(e.backups(t)); n != 2 {
		t.Errorf("%d backups, want 2", n)
	}
}
