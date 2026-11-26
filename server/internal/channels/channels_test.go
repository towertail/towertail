package channels

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/store"
	"github.com/towertail/towertail/server/wire"
)

type fakeStore struct{ list []store.Channel }

func (f fakeStore) ListChannels(context.Context, uuid.UUID) ([]store.Channel, error) {
	return f.list, nil
}

// hook records the requests and replies with the next status of codes
// (200 when there are no more).
type hook struct {
	mu    sync.Mutex
	reqs  []*http.Request
	body  [][]byte
	codes []int
	srv   *httptest.Server
}

func newHook(t *testing.T, codes ...int) *hook {
	h := &hook{codes: codes}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.reqs = append(h.reqs, r)
		h.body = append(h.body, b)
		code := http.StatusOK
		if len(h.codes) > 0 {
			code, h.codes = h.codes[0], h.codes[1:]
		}
		h.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.reqs)
}

func (h *hook) json(t *testing.T, i int) map[string]any {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(h.body[i], &m); err != nil {
		t.Fatalf("body %d: %v", i, err)
	}
	return m
}

func newTestRouter(list ...store.Channel) *Router {
	r := NewRouter(fakeStore{list: list}, config.NotifyConfig{}, nil, nil, slog.New(slog.DiscardHandler))
	r.delays = []time.Duration{time.Millisecond, time.Millisecond}
	return r
}

func testAlert() notify.Alert {
	return notify.Alert{
		EventID: uuid.New(), OrgID: uuid.New(), NodeID: uuid.New(), NodeName: "web-1", Tags: []string{"prod"},
		Metric: "cpu", From: wire.TintWarn, To: wire.TintCritical, Value: 0.934, At: time.Unix(1700000000, 0).UTC(),
	}
}

func channel(kind store.ChannelKind, target string) store.Channel {
	return store.Channel{ID: uuid.New(), Kind: kind, Name: string(kind), Enabled: true, Target: target}
}

func TestSlack(t *testing.T) {
	h := newHook(t)
	r := newTestRouter(channel(store.ChannelSlack, h.srv.URL))
	if err := r.Notify(context.Background(), testAlert()); err != nil {
		t.Fatal(err)
	}
	m := h.json(t, 0)
	if m["text"] != "web-1: CPU critical" {
		t.Errorf("text: %v", m["text"])
	}
	att := m["attachments"].([]any)[0].(map[string]any)
	if att["color"] != "#E5484D" || att["text"] != "CPU 93% (was warn)" {
		t.Errorf("attachment: %v", att)
	}
}

// Each channel shows the Detail of a long-process alert.
func TestLongProcDetail(t *testing.T) {
	const detail = "OrbStack Helper (pid 812) at 85% CPU for 32m."
	slack, discord, pd, wh := newHook(t), newHook(t), newHook(t), newHook(t)
	r := newTestRouter(channel(store.ChannelSlack, slack.srv.URL), channel(store.ChannelDiscord, discord.srv.URL),
		channel(store.ChannelPagerDuty, "key"), channel(store.ChannelWebhook, wh.srv.URL))
	r.senders[store.ChannelPagerDuty] = pagerDuty{p: newPoster(false), url: pd.srv.URL}
	a := testAlert()
	a.Metric, a.Instance, a.From, a.To, a.Value, a.Detail = "long_proc", "812/1799000000", wire.TintOK, wire.TintWarn, 85, detail
	if err := r.Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if att := slack.json(t, 0)["attachments"].([]any)[0].(map[string]any); att["text"] != detail {
		t.Errorf("slack: %v", att)
	}
	if e := discord.json(t, 0)["embeds"].([]any)[0].(map[string]any); e["description"] != detail {
		t.Errorf("discord: %v", e)
	}
	m := pd.json(t, 0)
	if p := m["payload"].(map[string]any); p["summary"] != "web-1: long-running process - "+detail {
		t.Errorf("pagerduty: %v", p)
	}
	if !strings.HasSuffix(m["dedup_key"].(string), ":long_proc:812/1799000000") {
		t.Errorf("pagerduty dedup key: %v", m["dedup_key"])
	}
	if w := wh.json(t, 0); w["text"] != detail || w["metric"] != "long_proc" {
		t.Errorf("webhook: %v", w)
	}
}

func TestDiscord(t *testing.T) {
	h := newHook(t)
	r := newTestRouter(channel(store.ChannelDiscord, h.srv.URL))
	a := testAlert()
	a.From, a.To = wire.TintCritical, wire.TintOK
	if err := r.Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	e := h.json(t, 0)["embeds"].([]any)[0].(map[string]any)
	if e["title"] != "web-1: CPU back to ok" || e["color"] != float64(0x30A46C) || e["timestamp"] != "2023-11-14T22:13:20Z" {
		t.Errorf("embed: %v", e)
	}
}

func TestPagerDuty(t *testing.T) {
	h := newHook(t)
	key := strings.Repeat("k", 32)
	r := newTestRouter(channel(store.ChannelPagerDuty, key))
	r.senders[store.ChannelPagerDuty] = pagerDuty{p: newPoster(false), url: h.srv.URL}
	a := testAlert()
	if err := r.Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	a.From, a.To = wire.TintCritical, wire.TintOK
	if err := r.Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	trig, res := h.json(t, 0), h.json(t, 1)
	if trig["routing_key"] != key || trig["event_action"] != "trigger" || trig["dedup_key"] != a.DedupKey() {
		t.Errorf("trigger: %v", trig)
	}
	if p := trig["payload"].(map[string]any); p["severity"] != "critical" || p["source"] != "web-1" {
		t.Errorf("payload: %v", p)
	}
	if res["event_action"] != "resolve" || res["dedup_key"] != a.DedupKey() || res["payload"] != nil {
		t.Errorf("resolve: %v", res)
	}
}

func TestWebhookSignature(t *testing.T) {
	h := newHook(t)
	ch := channel(store.ChannelWebhook, h.srv.URL)
	ch.Secret = "s3cret"
	r := newTestRouter(ch)
	a := testAlert()
	if err := r.Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	req := h.reqs[0]
	if req.Header.Get("X-Towertail-Event") != "alert" {
		t.Errorf("event header: %q", req.Header.Get("X-Towertail-Event"))
	}
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write([]byte(req.Header.Get("X-Towertail-Timestamp") + "."))
	m.Write(h.body[0])
	if want := "sha256=" + hex.EncodeToString(m.Sum(nil)); req.Header.Get("X-Towertail-Signature") != want {
		t.Errorf("signature: %q, want %q", req.Header.Get("X-Towertail-Signature"), want)
	}
	var p wire.AlertPayload
	if err := json.Unmarshal(h.body[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.V != 1 || p.Type != "alert" || p.EventID != a.EventID || p.NodeName != "web-1" || p.To != wire.TintCritical {
		t.Errorf("payload: %+v", p)
	}
}

func TestRetry(t *testing.T) {
	t.Run("5xx then ok", func(t *testing.T) {
		h := newHook(t, 503, 502)
		if err := newTestRouter(channel(store.ChannelSlack, h.srv.URL)).Notify(context.Background(), testAlert()); err != nil {
			t.Fatal(err)
		}
		if h.count() != 3 {
			t.Errorf("attempts: %d, want 3", h.count())
		}
	})
	t.Run("4xx is final", func(t *testing.T) {
		h := newHook(t, 404)
		err := newTestRouter(channel(store.ChannelSlack, h.srv.URL)).Notify(context.Background(), testAlert())
		if err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("err: %v", err)
		}
		if h.count() != 1 {
			t.Errorf("attempts: %d, want 1", h.count())
		}
	})
	t.Run("429 retries, then gives up", func(t *testing.T) {
		h := newHook(t, 429, 429, 429, 429)
		if err := newTestRouter(channel(store.ChannelSlack, h.srv.URL)).Notify(context.Background(), testAlert()); err == nil {
			t.Fatal("want error")
		}
		if h.count() != 3 {
			t.Errorf("attempts: %d, want 3", h.count())
		}
	})
}

func TestRouting(t *testing.T) {
	h := newHook(t)
	all := channel(store.ChannelWebhook, h.srv.URL)
	off := channel(store.ChannelWebhook, h.srv.URL)
	off.Enabled = false
	diskOnly := channel(store.ChannelWebhook, h.srv.URL)
	diskOnly.Route.Metrics = []string{"disk"}
	stage := channel(store.ChannelWebhook, h.srv.URL)
	stage.Route.Tags = []string{"stage"}
	critical := channel(store.ChannelWebhook, h.srv.URL)
	critical.Route.MinTint = wire.TintCritical
	r := newTestRouter(all, off, diskOnly, stage, critical)
	if err := r.Notify(context.Background(), testAlert()); err != nil {
		t.Fatal(err)
	}
	if h.count() != 2 {
		t.Errorf("sends: %d, want 2 (all and critical)", h.count())
	}
}

func TestBlockPrivateTargets(t *testing.T) {
	h := newHook(t)
	err := newPoster(true).post(context.Background(), h.srv.URL, map[string]string{}, nil)
	if !errors.Is(err, errBlockedTarget) || retryable(err) {
		t.Fatalf("err: %v", err)
	}
	if h.count() != 0 {
		t.Error("the request reached the server")
	}
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "192.168.1.1": true, "172.16.0.1": true, "169.254.169.254": true,
		"100.100.1.1": true, "::1": true, "fd00::1": true, "fe80::1": true, "0.0.0.0": true,
		"1.1.1.1": false, "2606:4700::1111": false,
	} {
		if got := blockedIP(net.ParseIP(ip)); got != want {
			t.Errorf("%s: %v, want %v", ip, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	r := newTestRouter()
	ok := []store.Channel{
		channel(store.ChannelSlack, "https://hooks.slack.com/services/T/B/X"),
		channel(store.ChannelPagerDuty, strings.Repeat("a", 32)),
	}
	for _, ch := range ok {
		if err := r.Validate(ch); err != nil {
			t.Errorf("%s: %v", ch.Kind, err)
		}
	}
	bad := []store.Channel{
		channel(store.ChannelSlack, "ftp://x"),
		channel(store.ChannelPagerDuty, "short"),
		channel(store.ChannelEmail, "ops@example.com"), // no SMTP host
		channel("sms", "x"),
		{Kind: store.ChannelSlack, Target: "https://x"}, // no name
		func() store.Channel {
			c := channel(store.ChannelSlack, "https://x")
			c.Route.MinTint = "ok"
			return c
		}(),
	}
	for _, ch := range bad {
		if err := r.Validate(ch); err == nil {
			t.Errorf("%+v: want error", ch)
		}
	}
}

// smtpServer is a small SMTP server without TLS. rcptCode is the reply
// to RCPT TO.
type smtpServer struct {
	ln       net.Listener
	rcptCode string
	msgs     atomic.Value // string
	rcpts    atomic.Int32
}

func newSMTPServer(t *testing.T, rcptCode string) *smtpServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpServer{ln: ln, rcptCode: rcptCode}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *smtpServer) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(l string) { _, _ = io.WriteString(c, l+"\r\n") }
	say("220 test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250-test")
			say("250 8BITMIME")
		case strings.HasPrefix(cmd, "MAIL"):
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT"):
			s.rcpts.Add(1)
			say(s.rcptCode + " rcpt")
		case cmd == "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.msgs.Store(b.String())
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (s *smtpServer) cfg(tlsMode string) config.SMTPConfig {
	addr := s.ln.Addr().(*net.TCPAddr)
	return config.SMTPConfig{Host: "127.0.0.1", Port: addr.Port, From: "Towertail <alerts@example.com>", TLS: tlsMode}
}

func TestEmail(t *testing.T) {
	srv := newSMTPServer(t, "250")
	ch := channel(store.ChannelEmail, "ops@example.com, Dev <dev@example.com>")
	err := email{cfg: srv.cfg("none")}.send(context.Background(), ch, testAlert())
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := srv.msgs.Load().(string)
	for _, want := range []string{
		"From: \"Towertail\" <alerts@example.com>\r\n",
		"To: ops@example.com, dev@example.com\r\n",
		"Subject: web-1: CPU critical\r\n",
		"Auto-Submitted: auto-generated\r\n",
		"\r\n\r\nweb-1: CPU critical\r\n\r\nCPU 93% (was warn)\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message has no %q:\n%s", want, msg)
		}
	}
	if srv.rcpts.Load() != 2 {
		t.Errorf("rcpts: %d", srv.rcpts.Load())
	}
}

func TestEmailErrors(t *testing.T) {
	t.Run("no STARTTLS", func(t *testing.T) {
		srv := newSMTPServer(t, "250")
		err := email{cfg: srv.cfg("starttls")}.send(context.Background(), channel(store.ChannelEmail, "a@example.com"), testAlert())
		if err == nil || !strings.Contains(err.Error(), "STARTTLS") || retryable(err) {
			t.Fatalf("err: %v", err)
		}
	})
	t.Run("5xx is final", func(t *testing.T) {
		srv := newSMTPServer(t, "550")
		err := email{cfg: srv.cfg("none")}.send(context.Background(), channel(store.ChannelEmail, "a@example.com"), testAlert())
		if err == nil || retryable(err) {
			t.Fatalf("err: %v", err)
		}
	})
	t.Run("4xx retries", func(t *testing.T) {
		srv := newSMTPServer(t, "451")
		err := email{cfg: srv.cfg("none")}.send(context.Background(), channel(store.ChannelEmail, "a@example.com"), testAlert())
		if err == nil || !retryable(err) {
			t.Fatalf("err: %v", err)
		}
	})
}

func TestTestSend(t *testing.T) {
	h := newHook(t)
	r := newTestRouter()
	if err := r.Test(context.Background(), channel(store.ChannelWebhook, h.srv.URL)); err != nil {
		t.Fatal(err)
	}
	m := h.json(t, 0)
	if m["type"] != "test" || m["title"] != "[test] test-node: CPU warn" {
		t.Errorf("test payload: %v", m)
	}
}

type fakeMailer struct {
	to    []string
	alert notify.Alert
	err   error
}

func (m *fakeMailer) SendAlert(_ context.Context, to []string, a notify.Alert) error {
	m.to = to
	m.alert = a
	return m.err
}

func TestMailer(t *testing.T) {
	m := &fakeMailer{}
	r := NewRouter(fakeStore{}, config.NotifyConfig{SMTP: config.SMTPConfig{Host: "smtp.example.com"}}, m, nil, slog.New(slog.DiscardHandler))
	ch := channel(store.ChannelEmail, "ops@example.com, Dev <dev@example.com>")
	if err := r.Validate(ch); err != nil {
		t.Fatal(err)
	}
	if err := r.Test(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	if len(m.to) != 2 || m.to[1] != "dev@example.com" {
		t.Errorf("to: %v", m.to)
	}
	s := r.senders[store.ChannelEmail]
	m.err = errors.New("502")
	if err := s.send(context.Background(), ch, testAlert()); !retryable(err) {
		t.Errorf("temporary error: %v", err)
	}
	m.err = fmt.Errorf("bad address: %w", notify.ErrPermanent)
	if err := s.send(context.Background(), ch, testAlert()); err == nil || retryable(err) {
		t.Errorf("permanent error: %v", err)
	}
}

type blockKind store.ChannelKind

func (b blockKind) ChannelAllowed(_ context.Context, _ uuid.UUID, k store.ChannelKind) bool {
	return k != store.ChannelKind(b)
}

func TestRouterSkipsBlockedKind(t *testing.T) {
	for _, tc := range []struct {
		name     string
		gate     Gate
		wantHook int
	}{
		{"blocked", blockKind(store.ChannelWebhook), 0},
		{"nil gate", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, m := newHook(t), &fakeMailer{}
			list := []store.Channel{channel(store.ChannelWebhook, h.srv.URL), channel(store.ChannelEmail, "ops@example.com")}
			r := NewRouter(fakeStore{list: list}, config.NotifyConfig{}, m, tc.gate, slog.New(slog.DiscardHandler))
			if err := r.Notify(context.Background(), testAlert()); err != nil {
				t.Fatal(err)
			}
			if got := h.count(); got != tc.wantHook {
				t.Errorf("webhook sends = %d, want %d", got, tc.wantHook)
			}
			if len(m.to) != 1 {
				t.Errorf("email recipients = %v, want 1", m.to)
			}
		})
	}
}

type lineJournal struct {
	mu    sync.Mutex
	lines []notify.Line
}

func (j *lineJournal) Write(_ context.Context, lines ...notify.Line) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, lines...)
	return nil
}

func TestRouterJournalLine(t *testing.T) {
	good, bad := newHook(t), newHook(t, http.StatusBadRequest)
	off := channel(store.ChannelWebhook, good.srv.URL)
	off.Name, off.Enabled = "off", false
	slack, hookCh := channel(store.ChannelSlack, good.srv.URL), channel(store.ChannelWebhook, bad.srv.URL)
	slack.Name, hookCh.Name = "Slack #ops-alerts", "billing hook"
	r := newTestRouter(slack, off, hookCh)
	j := &lineJournal{}
	r.UseJournal(j)

	a := testAlert()
	a.AlertID = uuid.New()
	if err := r.Notify(context.Background(), a); err == nil {
		t.Fatal("no error for the failed channel")
	}
	if len(j.lines) != 1 {
		t.Fatalf("lines = %+v, want one", j.lines)
	}
	l := j.lines[0]
	if l.Kind != notify.LineNotified || l.AlertID != a.AlertID || l.OrgID != a.OrgID || l.NodeID != a.NodeID ||
		l.Text != "Slack #ops-alerts, billing hook (failed)" {
		t.Fatalf("line = %+v", l)
	}

	// No line for a test alert, an alert with no id, or no matching channel.
	for name, mod := range map[string]func(*notify.Alert){
		"test":  func(a *notify.Alert) { a.Test = true },
		"no id": func(a *notify.Alert) { a.AlertID = uuid.Nil },
	} {
		b := a
		mod(&b)
		_ = r.Notify(context.Background(), b)
		if len(j.lines) != 1 {
			t.Fatalf("%s: lines = %+v", name, j.lines)
		}
	}
	empty := newTestRouter()
	empty.UseJournal(j)
	if err := empty.Notify(context.Background(), a); err != nil || len(j.lines) != 1 {
		t.Fatalf("no channel: %v, lines = %+v", err, j.lines)
	}
}

type fakeLinks struct{ ask, fewer string }

func (f fakeLinks) AskURL(notify.Alert) string   { return f.ask }
func (f fakeLinks) FewerURL(notify.Alert) string { return f.fewer }

func TestChatLinks(t *testing.T) {
	links := fakeLinks{ask: "https://app.example.com/?ask=a&b=<1>", fewer: "https://app.example.com/f?x=(1) 2"}
	for _, tc := range []struct {
		name        string
		links       notify.Links
		emails      int
		test        bool
		wantSlack   string
		wantDiscord string
	}{
		{"none", nil, 0, false, "CPU 93% (was warn)", "CPU 93% (was warn)"},
		{
			"links", links, 0, false,
			"CPU 93% (was warn)\n<https://app.example.com/?ask=a&amp;b=&lt;1&gt;|✦ Ask why> · <https://app.example.com/f?x=(1) 2|Get fewer of these →>",
			"CPU 93% (was warn)\n[✦ Ask why](https://app.example.com/?ask=a&b=<1>) · [Get fewer of these →](https://app.example.com/f?x=%281%29%202)",
		},
		{"count only", nil, 96, false, "CPU 93% (was warn)\n96 emails in 30 d", "CPU 93% (was warn)\n96 emails in 30 d"},
		{"one email", nil, 1, false, "CPU 93% (was warn)\n1 email in 30 d", "CPU 93% (was warn)\n1 email in 30 d"},
		{
			"links and count", fakeLinks{ask: "https://x.test/a"}, 5, false,
			"CPU 93% (was warn)\n<https://x.test/a|✦ Ask why> · 5 emails in 30 d",
			"CPU 93% (was warn)\n[✦ Ask why](https://x.test/a) · 5 emails in 30 d",
		},
		{"test alert", links, 5, true, "CPU 93% (was warn)", "CPU 93% (was warn)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh, dh := newHook(t), newHook(t)
			var opts []RouterOption
			if tc.links != nil {
				opts = append(opts, WithLinks(tc.links))
			}
			list := []store.Channel{channel(store.ChannelSlack, sh.srv.URL), channel(store.ChannelDiscord, dh.srv.URL)}
			r := NewRouter(fakeStore{list: list}, config.NotifyConfig{}, nil, nil, slog.New(slog.DiscardHandler), opts...)
			a := testAlert()
			a.RuleEmails, a.Test = tc.emails, tc.test
			for _, ch := range list {
				if err := r.senders[ch.Kind].send(context.Background(), ch, a); err != nil {
					t.Fatal(err)
				}
			}
			if att := sh.json(t, 0)["attachments"].([]any)[0].(map[string]any); att["text"] != tc.wantSlack {
				t.Errorf("slack text = %q, want %q", att["text"], tc.wantSlack)
			}
			if e := dh.json(t, 0)["embeds"].([]any)[0].(map[string]any); e["description"] != tc.wantDiscord {
				t.Errorf("discord description = %q, want %q", e["description"], tc.wantDiscord)
			}
		})
	}
}

func TestEmailRuleCount(t *testing.T) {
	srv := newSMTPServer(t, "250")
	ch := channel(store.ChannelEmail, "ops@example.com")
	a := testAlert()
	for _, tc := range []struct {
		n    int
		want string
	}{{0, ""}, {96, "This rule sent 96 emails in 30 days."}, {1, "This rule sent 1 email in 30 days."}} {
		a.RuleEmails = tc.n
		if err := (email{cfg: srv.cfg("none")}).send(context.Background(), ch, a); err != nil {
			t.Fatal(err)
		}
		msg, _ := srv.msgs.Load().(string)
		if has := strings.Contains(msg, "This rule sent"); has != (tc.want != "") || !strings.Contains(msg, tc.want) {
			t.Errorf("n=%d: message:\n%s", tc.n, msg)
		}
	}
}

type fakeCounter struct {
	mu    sync.Mutex
	calls []RuleSendsQuery
	n     int
	err   error
}

func (f *fakeCounter) RuleSends(_ context.Context, q RuleSendsQuery) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, q)
	return f.n, f.err
}

func (f *fakeCounter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newCountRouter(c SendCounter, m notify.Mailer, list ...store.Channel) *Router {
	r := NewRouter(fakeStore{list: list}, config.NotifyConfig{}, m, nil, slog.New(slog.DiscardHandler), WithSendCounter(c))
	r.delays = []time.Duration{time.Millisecond, time.Millisecond}
	return r
}

func TestRouterRuleEmails(t *testing.T) {
	m, c := &fakeMailer{}, &fakeCounter{n: 96}
	off := channel(store.ChannelEmail, "off@example.com")
	off.Name, off.Enabled = "Email off", false
	on := channel(store.ChannelEmail, "ops@example.com")
	on.Name = "Email ops"
	r := newCountRouter(c, m, off, on)
	a := testAlert()
	before := time.Now()
	if err := r.Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if m.alert.RuleEmails != 96 {
		t.Errorf("RuleEmails = %d, want 96", m.alert.RuleEmails)
	}
	if len(c.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(c.calls))
	}
	q := c.calls[0]
	if q.OrgID != a.OrgID || q.NodeID != a.NodeID || q.Metric != "cpu" {
		t.Errorf("query = %+v", q)
	}
	if len(q.Channels) != 2 || q.Channels[0] != "Email off" || q.Channels[1] != "Email ops" {
		t.Errorf("channels = %v", q.Channels)
	}
	if d := before.Add(-sendWindow).Sub(q.Since); d > time.Second || d < -time.Second {
		t.Errorf("since = %v", q.Since)
	}
	// The send adds one to the cached count, with no new query.
	if err := r.Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if m.alert.RuleEmails != 97 || c.count() != 1 {
		t.Errorf("second alert: RuleEmails = %d, calls = %d", m.alert.RuleEmails, c.count())
	}
}

func TestRouterRuleEmailsSkips(t *testing.T) {
	wh := newHook(t)
	offEmail := channel(store.ChannelEmail, "ops@example.com")
	offEmail.Enabled = false
	hook := channel(store.ChannelWebhook, wh.srv.URL)
	slackOnly := []store.Channel{channel(store.ChannelSlack, wh.srv.URL)}
	for _, tc := range []struct {
		name string
		list []store.Channel
	}{
		{"webhook only", []store.Channel{offEmail, hook}},
		{"slack and no email channel", slackOnly},
		{"no channel", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeCounter{n: 5}
			if err := newCountRouter(c, &fakeMailer{}, tc.list...).Notify(context.Background(), testAlert()); err != nil {
				t.Fatal(err)
			}
			if c.count() != 0 {
				t.Errorf("calls = %d, want 0", c.count())
			}
		})
	}
	t.Run("test alert", func(t *testing.T) {
		c := &fakeCounter{n: 5}
		m := &fakeMailer{}
		r := newCountRouter(c, m, channel(store.ChannelEmail, "ops@example.com"))
		a := testAlert()
		a.Test = true
		if err := r.Notify(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		if c.count() != 0 || m.alert.RuleEmails != 0 {
			t.Errorf("calls = %d, RuleEmails = %d", c.count(), m.alert.RuleEmails)
		}
	})
}

func TestRouterRuleEmailsError(t *testing.T) {
	m, c := &fakeMailer{}, &fakeCounter{err: errors.New("clickhouse down")}
	r := newCountRouter(c, m, channel(store.ChannelEmail, "ops@example.com"))
	a := testAlert()
	for range 2 {
		if err := r.Notify(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.to) != 1 || m.alert.RuleEmails != 1 {
		t.Errorf("to = %v, RuleEmails = %d", m.to, m.alert.RuleEmails)
	}
	if c.count() != 1 {
		t.Errorf("calls = %d, want 1 (the error is cached)", c.count())
	}
}

func TestRouterFailedEmailNotCounted(t *testing.T) {
	m, c := &fakeMailer{err: fmt.Errorf("bad: %w", notify.ErrPermanent)}, &fakeCounter{n: 3}
	r := newCountRouter(c, m, channel(store.ChannelEmail, "ops@example.com"))
	a := testAlert()
	if err := r.Notify(context.Background(), a); err == nil {
		t.Fatal("want an error")
	}
	m.err = nil
	if err := r.Notify(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if m.alert.RuleEmails != 3 {
		t.Errorf("RuleEmails = %d, want 3", m.alert.RuleEmails)
	}
}
