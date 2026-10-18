package channels

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	r := NewRouter(fakeStore{list: list}, config.NotifyConfig{}, slog.New(slog.DiscardHandler))
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
