package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/auth"
	"github.com/towertail/towertail/server/store"
)

type maxNodes int

func (maxNodes) HistoryDays(context.Context, uuid.UUID) int { return 0 }
func (m maxNodes) MaxNodes(context.Context, uuid.UUID) int  { return int(m) }

func TestNodeLimit(t *testing.T) {
	st, err := store.OpenBolt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authctx.Set(c, store.Token{OrgID: store.ZeroOrg, Kind: store.TokenKindUser})
			return next(c)
		}
	}
	srv := New(Deps{
		Config:     &config.Config{},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:      st,
		Issuer:     auth.NewIssuer(st),
		Middleware: []echo.MiddlewareFunc{user},
		Limits:     maxNodes(2),
	})
	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	a, b := uuid.NewString(), uuid.NewString()
	for _, c := range []struct {
		method, target, body string
		want                 int
	}{
		{http.MethodPost, "/v1/nodes", `{"id":"` + a + `","displayName":"a"}`, http.StatusCreated},
		{http.MethodPut, "/v1/nodes/" + b, `{"displayName":"b"}`, http.StatusOK},
		{http.MethodPost, "/v1/nodes", `{"displayName":"c"}`, http.StatusPaymentRequired},
		{http.MethodPut, "/v1/nodes/" + uuid.NewString(), `{"displayName":"d"}`, http.StatusPaymentRequired},
		{http.MethodPost, "/v1/sampler/enroll", `{"display_name":"e"}`, http.StatusPaymentRequired},
		// An update of a node that exists passes.
		{http.MethodPut, "/v1/nodes/" + a, `{"displayName":"a2"}`, http.StatusOK},
		{http.MethodPost, "/v1/nodes", `{"id":"` + b + `","displayName":"b2"}`, http.StatusCreated},
	} {
		rec := do(c.method, c.target, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s: %d, want %d: %s", c.method, c.target, rec.Code, c.want, rec.Body.String())
		}
		if c.want == http.StatusPaymentRequired && !strings.Contains(rec.Body.String(), `"plan_limit"`) {
			t.Errorf("402 body: %s", rec.Body.String())
		}
	}
}

func TestUpdateNodeKeepsHideProcFolders(t *testing.T) {
	e := newScanEnv(t)
	path := "/v1/nodes/" + e.on.String()
	get := func() store.Node {
		var n store.Node
		decode(t, e.do(http.MethodGet, path, e.user, nil).Body.Bytes(), &n)
		return n
	}
	if get().HideProcFolders {
		t.Fatal("a node starts with folders on")
	}
	wantCode(t, e.do(http.MethodPut, path, e.user, []byte(`{"displayName":"a","kind":"ssh","hideProcFolders":true}`)), http.StatusOK)
	if !get().HideProcFolders {
		t.Fatal("hideProcFolders did not save")
	}
	// A client that does not send the key keeps the stored value.
	wantCode(t, e.do(http.MethodPut, path, e.user, []byte(`{"displayName":"b","kind":"ssh"}`)), http.StatusOK)
	if n := get(); !n.HideProcFolders || n.DisplayName != "b" {
		t.Fatalf("node: %+v", n)
	}
	wantCode(t, e.do(http.MethodPut, path, e.user, []byte(`{"displayName":"b","kind":"ssh","hideProcFolders":false}`)), http.StatusOK)
	if get().HideProcFolders {
		t.Fatal("hideProcFolders is still on")
	}
}

func TestUpdateNodeKeepsRuleFields(t *testing.T) {
	e := newAlertEnv(t)
	rules := `{"thresholds":{"pids":{"warn":0.5,"critical":0.6}},"alerts":{"cpu":{"sustainSeconds":60,"notify":"all"}},"muted":["disk"],"origin":{"client":"mac"}}`
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String()+"/rules", rules); code != http.StatusOK {
		t.Fatalf("put rules: %d %s", code, b)
	}
	want := e.nodeNow()
	// The GET-then-PUT of an old client has no rule keys.
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String(), `{"displayName":"web2"}`); code != http.StatusOK {
		t.Fatalf("update: %d %s", code, b)
	}
	got := e.nodeNow()
	if got.DisplayName != "web2" || got.ThresholdOverrides == nil || got.ThresholdOverrides.PIDs == nil ||
		got.AlertOverrides == nil || got.AlertOverrides.CPU.SustainSeconds != 60 ||
		!got.MutedSignals.Has(store.MetricDisk) || got.RulesOrigin == nil || !got.RulesOrigin.At.Equal(want.RulesOrigin.At) {
		t.Fatalf("rule fields lost: %+v", got)
	}
	// A key with null clears the field. A bad pair gives 400.
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String(), `{"displayName":"web2","thresholdOverrides":null,"alertOverrides":null}`); code != http.StatusOK {
		t.Fatalf("update: %d %s", code, b)
	}
	if got := e.nodeNow(); got.ThresholdOverrides != nil || got.AlertOverrides != nil || !got.MutedSignals.Has(store.MetricDisk) {
		t.Fatalf("not cleared: %+v", got)
	}
	bad := `{"displayName":"web3","thresholdOverrides":{"cpu":{"warn":0.9,"critical":0.5}}}`
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String(), bad); code != http.StatusBadRequest {
		t.Fatalf("bad pair: %d %s", code, b)
	}
	if got := e.nodeNow(); got.DisplayName != "web2" {
		t.Fatalf("a bad body wrote: %+v", got)
	}
	// A client that sends rulesOrigin gets the server user and time.
	body := `{"displayName":"web2","rulesOrigin":{"client":"windows","device":"pc","userId":"00000000-0000-0000-0000-000000000009"}}`
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String(), body); code != http.StatusOK {
		t.Fatalf("update: %d %s", code, b)
	}
	if o := e.nodeNow().RulesOrigin; o == nil || o.Client != "windows" || o.Device != "pc" || o.UserID.String() == "00000000-0000-0000-0000-000000000009" || time.Since(o.At) > time.Minute {
		t.Fatalf("origin = %+v", o)
	}
}

func TestUpdateNodeLegacyCustomThresholdsMerges(t *testing.T) {
	e := newAlertEnv(t)
	rules := `{"thresholds":{"pids":{"warn":0.5,"critical":0.6},"cpu":{"warn":0.1,"critical":0.2}}}`
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String()+"/rules", rules); code != http.StatusOK {
		t.Fatalf("put rules: %d %s", code, b)
	}
	body := `{"displayName":"web1","customThresholds":{"cpuWarn":0.6,"cpuCritical":0.7,"memWarn":0.5,"memCritical":0.6,"diskWarn":0.4,"diskCritical":0.5}}`
	if code, b := e.do(store.RoleAdmin, "PUT", "/v1/nodes/"+e.node.String(), body); code != http.StatusOK {
		t.Fatalf("update: %d %s", code, b)
	}
	o := e.nodeNow().ThresholdOverrides
	if o == nil || o.CPU.Warn != 0.6 || o.Mem.Warn != 0.5 || o.Disk.Critical != 0.5 {
		t.Fatalf("overrides = %+v", o)
	}
	if o.PIDs == nil || o.PIDs.Warn != 0.5 {
		t.Fatalf("health override lost: %+v", o)
	}
}
