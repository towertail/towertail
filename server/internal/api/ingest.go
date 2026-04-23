package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/server/internal/clickhouse"
	"github.com/towertail/server/internal/store"
	"github.com/towertail/server/internal/wire"
)

// handleIngest accepts NDJSON (optionally gzipped) and pushes each sample
// into the ClickHouse batcher.
func (s *Server) handleIngest(c echo.Context) error {
	t, ok := tokenFromCtx(c)
	if !ok || t.Kind != store.TokenKindSampler {
		return echo.NewHTTPError(http.StatusForbidden, "sampler token required")
	}
	if t.NodeID == nil {
		return echo.NewHTTPError(http.StatusForbidden, "token missing node binding")
	}
	// Lazy-require clickhouse on first ingest. Skipped when CH is disabled
	// (dev mode): samples fan out to the WS hub and through the alerter,
	// but nothing is persisted.
	if s.ch != nil {
		if err := s.runtime.Require(c.Request().Context(), "clickhouse"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse not ready")
		}
		if !s.ch.Ready() {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "clickhouse warming up")
		}
	}

	req := c.Request()
	bodyReader := http.MaxBytesReader(c.Response().Writer, req.Body, s.cfg.Ingest.MaxBodyBytes)
	defer bodyReader.Close()

	var r io.Reader = bodyReader
	if strings.EqualFold(req.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "bad gzip")
		}
		defer gz.Close()
		r = gz
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 128*1024), 4*1024*1024)
	var accepted, rejected int
	var firstErr string

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		bundle, err := decodeSample(line, t.OrgID, *t.NodeID)
		if err != nil {
			rejected++
			if firstErr == "" {
				firstErr = err.Error()
			}
			continue
		}
		if s.hub != nil {
			s.hub.PublishSample(t.OrgID, *t.NodeID, append([]byte(nil), line...))
		}
		if s.ch != nil {
			if !s.ch.Batcher().Enqueue(bundle) {
				rejected++
				continue
			}
		}
		s.evalAlerter(c.Request().Context(), t.OrgID, *t.NodeID, bundle)
		accepted++
	}
	if err := scanner.Err(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	return c.JSON(http.StatusAccepted, map[string]any{
		"accepted": accepted,
		"rejected": rejected,
		"error":    firstErr,
	})
}

// decodeSample parses a single NDJSON line into the IngestBundle the
// ClickHouse batcher expects. Returns an error for samples we can't
// decode or that have an unsupported schema version.
func decodeSample(line []byte, orgID, nodeID uuid.UUID) (clickhouse.IngestBundle, error) {
	var s wire.Sample
	if err := json.Unmarshal(line, &s); err != nil {
		return clickhouse.IngestBundle{}, err
	}
	if s.V != 1 {
		return clickhouse.IngestBundle{}, echoErrorf(http.StatusBadRequest, "unsupported schema v=%d", s.V)
	}
	ts, err := parseTS(s.TS)
	if err != nil {
		return clickhouse.IngestBundle{}, err
	}

	b := clickhouse.IngestBundle{}
	b.Sample = clickhouse.SampleRow{
		TS:            ts,
		OrgID:         orgID,
		NodeID:        nodeID,
		SchemaV:       uint8(s.V),
		HostName:      s.Host.Name,
		HostOS:        s.Host.OS,
		HostArch:      s.Host.Arch,
		HostKernel:    s.Host.Kernel,
		HostUptimeS:   uint64Positive(s.Host.UptimeS),
		HostSampler:   s.Host.Sampler,
		HostMachineID: s.Host.MachineID,
		CPUPct:        float32(s.CPU.Pct),
		CPULoad1:      float32(s.CPU.Load1),
		CPULoad5:      float32(s.CPU.Load5),
		CPULoad15:     float32(s.CPU.Load15),
		CPUCores:      uint16(s.CPU.Cores),
		MemUsed:       uint64Positive(s.Mem.Used),
		MemTotal:      uint64Positive(s.Mem.Total),
		SwapUsed:      uint64Positive(s.Swap.Used),
		SwapTotal:     uint64Positive(s.Swap.Total),
		Errors:        s.Errors,
	}
	if s.DiskIO != nil {
		b.Sample.DiskReadBps = uint64Positive(s.DiskIO.ReadBps)
		b.Sample.DiskWriteBps = uint64Positive(s.DiskIO.WriteBps)
		b.Sample.DiskReadCum = uint64Positive(s.DiskIO.ReadCum)
		b.Sample.DiskWriteCum = uint64Positive(s.DiskIO.WriteCum)
		for _, d := range s.DiskIO.Devices {
			b.DiskIO = append(b.DiskIO, clickhouse.DiskIORow{
				TS:       ts,
				OrgID:    orgID,
				NodeID:   nodeID,
				Device:   d.Name,
				ReadBps:  uint64Positive(d.ReadBps),
				WriteBps: uint64Positive(d.WriteBps),
				ReadCum:  uint64Positive(d.ReadCum),
				WriteCum: uint64Positive(d.WriteCum),
			})
		}
	}
	if s.Net != nil {
		b.Sample.NetRxBps = uint64Positive(s.Net.RxBps)
		b.Sample.NetTxBps = uint64Positive(s.Net.TxBps)
		b.Sample.NetRxCum = uint64Positive(s.Net.RxCum)
		b.Sample.NetTxCum = uint64Positive(s.Net.TxCum)
	}
	if s.Disks != nil {
		for _, d := range *s.Disks {
			b.Disks = append(b.Disks, clickhouse.DiskRow{
				TS:     ts,
				OrgID:  orgID,
				NodeID: nodeID,
				Mount:  d.Mount,
				FS:     d.Fs,
				Used:   uint64Positive(d.Used),
				Total:  uint64Positive(d.Total),
			})
		}
	}
	if s.Procs != nil {
		b.Sample.ProcsTotal = uint32Positive(s.Procs.Total)
		b.Sample.ProcsVisible = uint32Positive(s.Procs.Visible)
		if s.Procs.Root {
			b.Sample.ProcsRoot = 1
		}
		for _, p := range s.Procs.Items {
			var rb, wb *uint64
			if p.ReadBytes != nil {
				v := uint64Positive(*p.ReadBytes)
				rb = &v
			}
			if p.WriteBytes != nil {
				v := uint64Positive(*p.WriteBytes)
				wb = &v
			}
			pts, _ := parseTS(p.StartTS)
			b.Procs = append(b.Procs, clickhouse.ProcRow{
				TS:         ts,
				OrgID:      orgID,
				NodeID:     nodeID,
				PID:        p.PID,
				PPID:       p.PPID,
				Name:       p.Name,
				Cmd:        p.Cmd,
				UserName:   p.User,
				CPUPct:     float32(p.CPUPct),
				RSS:        uint64Positive(p.RSS),
				Threads:    p.Threads,
				State:      p.State,
				StartTS:    pts,
				ReadBytes:  rb,
				WriteBytes: wb,
			})
		}
	}
	return b, nil
}

func uint64Positive(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

func uint32Positive(v int) uint32 {
	if v < 0 {
		return 0
	}
	return uint32(v)
}

func parseTS(s string) (time.Time, error) {
	if s == "" {
		return time.Now().UTC(), nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, echoErrorf(http.StatusBadRequest, "invalid ts: %q", s)
}

func echoErrorf(status int, format string, args ...any) error {
	return echo.NewHTTPError(status, format)
}

// evalAlerter drives the in-memory threshold engine on a per-sample basis.
func (s *Server) evalAlerter(ctx context.Context, orgID, nodeID uuid.UUID, b clickhouse.IngestBundle) {
	if s.alerter == nil {
		return
	}
	cpu := float64(b.Sample.CPUPct) / 100.0
	var mem float64
	if b.Sample.MemTotal > 0 {
		mem = float64(b.Sample.MemUsed) / float64(b.Sample.MemTotal)
	}
	var maxDisk float64
	for _, d := range b.Disks {
		if d.Total > 0 {
			r := float64(d.Used) / float64(d.Total)
			if r > maxDisk {
				maxDisk = r
			}
		}
	}
	node, err := s.store.GetNode(ctx, orgID, nodeID)
	var n *store.Node
	if err == nil {
		n = &node
	}
	s.alerter.Evaluate(ctx, orgID, nodeID, n, cpu, mem, maxDisk)
}
