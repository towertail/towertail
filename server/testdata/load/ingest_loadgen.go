//go:build loadtest

// Package main is a load generator for /v1/ingest/samples. It acts as
// many hosts. Each host has its own sampler token (from
// `towertail-server token issue-bulk`) and sends one real payload each
// interval. Run with:
//
//	go run -tags=loadtest ./testdata/load \
//	  --url=http://localhost:8080/v1/ingest/samples \
//	  --tokens=tokens.csv --interval=10s --duration=30m
//
// Use a dedicated test server.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/towertail/towertail/server/wire"
)

func main() {
	url := flag.String("url", "http://localhost:8080/v1/ingest/samples", "ingest URL")
	tokens := flag.String("tokens", "tokens.csv", "CSV of node_id,token from token issue-bulk")
	interval := flag.Duration("interval", 10*time.Second, "time between two samples of a host")
	duration := flag.Duration("duration", 30*time.Minute, "run time")
	payload := flag.String("payload", "testdata/load/sample-linux.json", "sample JSON to send")
	jitter := flag.Duration("jitter", 2*time.Second, "random change of each wait, from -jitter/2 to +jitter/2")
	concurrency := flag.Int("concurrency", 512, "requests in flight at most")
	flag.Parse()

	toks, err := readTokens(*tokens)
	if err != nil {
		fail(err)
	}
	base, err := os.ReadFile(*payload)
	if err != nil {
		fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	g := &loadgen{
		url:      *url,
		interval: *interval,
		jitter:   *jitter,
		sem:      make(chan struct{}, *concurrency),
		client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{MaxIdleConns: *concurrency, MaxIdleConnsPerHost: *concurrency},
		},
	}
	fmt.Printf("hosts=%d interval=%s duration=%s concurrency=%d\n", len(toks), *interval, *duration, *concurrency)

	var wg sync.WaitGroup
	for i, tok := range toks {
		h, err := newHost(i, tok, base)
		if err != nil {
			fail(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.run(ctx, h)
		}()
	}
	go g.report(ctx, 10*time.Second)
	wg.Wait()
	fmt.Print("total ")
	g.total.print()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func readTokens(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		if len(r) < 2 || r[0] == "node_id" {
			continue
		}
		out = append(out, r[1])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no tokens", path)
	}
	return out, nil
}

// host is one simulated sampler with its own copy of the payload.
type host struct {
	token string
	s     wire.Sample
	cpu   []float64
	rng   *rand.Rand
}

func newHost(i int, token string, base []byte) (*host, error) {
	h := &host{token: token, rng: rand.New(rand.NewPCG(uint64(i), 1))}
	if err := json.Unmarshal(base, &h.s); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	h.s.Host.Name = fmt.Sprintf("load-%d", i+1)
	if h.s.Procs != nil {
		for _, p := range h.s.Procs.Items {
			h.cpu = append(h.cpu, p.CPUPct)
		}
	}
	return h, nil
}

// next changes the values a little, so the compression is real.
func (h *host) next() ([]byte, error) {
	r := h.rng
	h.s.TS = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	h.s.Host.UptimeS += 10
	h.s.CPU.Pct = clamp(h.s.CPU.Pct+r.NormFloat64()*3, 1, 99)
	h.s.CPU.Load1 = clamp(h.s.CPU.Load1+r.NormFloat64()*0.1, 0, 32)
	h.s.Mem.Used = int64(clamp(float64(h.s.Mem.Used)*(1+r.NormFloat64()*0.01), 1<<30, float64(h.s.Mem.Total)))
	if h.s.Net != nil {
		h.s.Net.RxBps = int64(clamp(float64(h.s.Net.RxBps)*(1+r.NormFloat64()*0.1), 0, 1e9))
		h.s.Net.RxCum += h.s.Net.RxBps * 10
	}
	if h.s.Procs != nil {
		for i := range h.s.Procs.Items {
			p := &h.s.Procs.Items[i]
			p.CPUPct = clamp(h.cpu[i]*(1+r.NormFloat64()*0.2), 0, 100)
			p.RSS = int64(float64(p.RSS) * (1 + r.NormFloat64()*0.001))
		}
	}
	line, err := json.Marshal(h.s)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(append(line, '\n'))
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func clamp(v, lo, hi float64) float64 { return min(max(v, lo), hi) }

type loadgen struct {
	url      string
	interval time.Duration
	jitter   time.Duration
	sem      chan struct{}
	client   *http.Client

	window, total stats
}

// run sends one sample of the host each interval until ctx is done. The
// first send has a random offset, so the hosts do not all send at once.
func (g *loadgen) run(ctx context.Context, h *host) {
	wait := time.Duration(h.rng.Int64N(int64(g.interval)))
	for {
		if g.jitter > 0 {
			wait = max(0, wait+time.Duration(h.rng.Int64N(int64(g.jitter)))-g.jitter/2)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		start := time.Now()
		g.send(ctx, h)
		wait = max(0, g.interval-time.Since(start))
	}
}

func (g *loadgen) send(ctx context.Context, h *host) {
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-g.sem }()

	body, err := h.next()
	if err != nil {
		g.record(0, 0, err)
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	start := time.Now()
	resp, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			g.record(0, 0, err)
		}
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	g.record(resp.StatusCode, time.Since(start), nil)
}

func (g *loadgen) record(code int, d time.Duration, err error) {
	g.window.add(code, d, err)
	g.total.add(code, d, err)
}

func (g *loadgen) report(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fmt.Printf("%s ", time.Now().Format("15:04:05"))
			g.window.print()
			g.window.reset()
		}
	}
}

// stats counts the results of the requests.
type stats struct {
	requests, ok, busy, other, errs atomic.Int64

	mu        sync.Mutex
	latencies []time.Duration
	lastErr   string
}

func (s *stats) add(code int, d time.Duration, err error) {
	s.requests.Add(1)
	switch {
	case err != nil:
		s.errs.Add(1)
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return
	case code >= 200 && code < 300:
		s.ok.Add(1)
	case code == http.StatusServiceUnavailable:
		s.busy.Add(1)
	default:
		s.other.Add(1)
	}
	s.mu.Lock()
	s.latencies = append(s.latencies, d)
	s.mu.Unlock()
}

func (s *stats) reset() {
	s.requests.Store(0)
	s.ok.Store(0)
	s.busy.Store(0)
	s.other.Store(0)
	s.errs.Store(0)
	s.mu.Lock()
	s.latencies = s.latencies[:0]
	s.lastErr = ""
	s.mu.Unlock()
}

func (s *stats) print() {
	s.mu.Lock()
	lat := slices.Clone(s.latencies)
	lastErr := s.lastErr
	s.mu.Unlock()
	slices.Sort(lat)
	fmt.Printf("requests=%d 2xx=%d 503=%d other=%d errors=%d p50=%s p99=%s",
		s.requests.Load(), s.ok.Load(), s.busy.Load(), s.other.Load(), s.errs.Load(),
		percentile(lat, 0.50), percentile(lat, 0.99))
	if lastErr != "" {
		fmt.Printf(" last_error=%q", lastErr)
	}
	fmt.Println()
}

func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(q*float64(len(sorted)-1))].Round(time.Millisecond)
}
