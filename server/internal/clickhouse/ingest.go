package clickhouse

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/towertail/towertail/server/config"
)

// ErrBusy is returned by Enqueue when the queue is full. The caller
// answers 503, and the sampler sends again later.
var ErrBusy = errors.New("clickhouse: ingest queue full")

// queueSize is the capacity of the batcher queue, in bundles.
const queueSize = 4096

// inserter writes bundles to ClickHouse. Tests replace it.
type inserter interface {
	insert(ctx context.Context, bundles []IngestBundle) error
}

// batcher aggregates IngestBundles and flushes them in grouped batches.
// One writer goroutine (v1: a single shard is plenty at 10k-host
// scale). Flushes on row count, byte estimate, or period. A batch that
// fails after the retries goes to the spill folder, and the next good
// flush replays it.
type batcher struct {
	cfg    config.ClickHouseConfig
	ins    inserter
	spill  *spill
	log    *slog.Logger
	delays []time.Duration

	enqMu  sync.Mutex
	in     chan IngestBundle
	wg     sync.WaitGroup
	stopCh chan struct{}
}

func newBatcher(cfg config.ClickHouseConfig, ins inserter, sp *spill, log *slog.Logger) *batcher {
	return &batcher{
		cfg:    cfg,
		ins:    ins,
		spill:  sp,
		log:    log,
		delays: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second},
		in:     make(chan IngestBundle, queueSize),
		stopCh: make(chan struct{}),
	}
}

func (b *batcher) start() {
	b.wg.Add(1)
	go b.run()
}

func (b *batcher) stop() {
	close(b.stopCh)
	b.wg.Wait()
}

// Enqueue submits the bundles of one request. It takes all of them or,
// with ErrBusy, none, so a retry of the request adds no duplicate rows.
// More bundles than the queue holds go in parts.
func (b *batcher) Enqueue(bundles ...IngestBundle) error {
	b.enqMu.Lock()
	defer b.enqMu.Unlock()
	for len(bundles) > 0 {
		n := min(len(bundles), queueSize)
		// Only Enqueue adds to the queue, and it holds enqMu, so the free
		// space can only grow before the sends.
		if cap(b.in)-len(b.in) < n {
			return ErrBusy
		}
		for _, bundle := range bundles[:n] {
			b.in <- bundle
		}
		bundles = bundles[n:]
	}
	batcherQueueDepth.Set(float64(len(b.in)))
	return nil
}

func (b *batcher) run() {
	defer b.wg.Done()
	period := b.cfg.BatchFlushPeriod
	if period <= 0 {
		period = time.Second
	}
	maxRows := b.cfg.BatchFlushRows
	if maxRows <= 0 {
		maxRows = 5000
	}
	maxBytes := b.cfg.BatchFlushBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}

	var (
		pending []IngestBundle
		bytes   int
	)
	tick := time.NewTicker(period)
	defer tick.Stop()

	flush := func(reason string) {
		if len(pending) > 0 {
			b.flushPending(reason, pending)
		}
		pending = nil
		bytes = 0
		batcherQueueDepth.Set(float64(len(b.in)))
	}

	for {
		select {
		case <-b.stopCh:
			for len(b.in) > 0 {
				pending = append(pending, <-b.in)
			}
			flush("stop")
			return
		case bundle := <-b.in:
			pending = append(pending, bundle)
			// Rough byte estimate, avoids JSON-marshal overhead in hot path.
			bytes += 256 + 64*len(bundle.Disks) + 80*len(bundle.DiskIO) + 256*len(bundle.Procs)
			if len(pending) >= maxRows || bytes >= maxBytes {
				flush("size")
			}
		case <-tick.C:
			flush("tick")
		}
	}
}

// flushPending inserts the batch with retries. On a failure it spills
// the batch. On a success it replays one spill file.
func (b *batcher) flushPending(reason string, pending []IngestBundle) {
	start := time.Now()
	err := b.insert(pending)
	for i := 0; err != nil && i < len(b.delays); i++ {
		batcherFlushErrors.Inc()
		b.log.Warn("clickhouse batcher: flush failed, retry", "reason", reason, "attempt", i+1, "err", err)
		if !b.wait(b.delays[i]) {
			break
		}
		err = b.insert(pending)
	}
	if err != nil {
		batcherFlushErrors.Inc()
		b.log.Error("clickhouse batcher: flush failed, spill", "reason", reason, "bundles", len(pending), "err", err)
		if err := b.spill.write(pending); err != nil {
			b.log.Error("clickhouse batcher: spill failed, rows lost", "bundles", len(pending), "err", err)
		}
		return
	}
	batcherFlushSeconds.Observe(time.Since(start).Seconds())
	batcherFlushRows.Observe(float64(rowCount(pending)))
	b.log.Debug("clickhouse batcher: flushed", "reason", reason, "bundles", len(pending))
	b.replay()
}

// replay inserts the oldest spill file once. It keeps the file when the
// insert fails.
func (b *batcher) replay() {
	path, ok := b.spill.oldest()
	if !ok {
		return
	}
	bundles, err := b.spill.read(path)
	if err != nil {
		b.log.Error("clickhouse batcher: bad spill file", "path", path, "err", err)
		b.spill.reject(path)
		return
	}
	if err := b.insert(bundles); err != nil {
		b.log.Warn("clickhouse batcher: spill replay failed", "path", path, "err", err)
		return
	}
	b.spill.remove(path)
	b.log.Info("clickhouse batcher: spill replayed", "path", path, "bundles", len(bundles))
}

func (b *batcher) insert(bundles []IngestBundle) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return b.ins.insert(ctx, bundles)
}

// wait sleeps for d. It returns false at once when the batcher stops,
// so the stop flush spills without the retry delays.
func (b *batcher) wait(d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-b.stopCh:
		return false
	}
}

func rowCount(bundles []IngestBundle) int {
	n := 0
	for _, bd := range bundles {
		n += 1 + len(bd.Disks) + len(bd.DiskIO) + len(bd.Procs)
	}
	return n
}

// insert writes the rows of the bundles to their tables.
func (c *Client) insert(ctx context.Context, bundles []IngestBundle) error {
	var (
		samples []SampleRow
		disks   []DiskRow
		ios     []DiskIORow
		procs   []ProcRow
	)
	for _, bd := range bundles {
		samples = append(samples, bd.Sample)
		disks = append(disks, bd.Disks...)
		ios = append(ios, bd.DiskIO...)
		procs = append(procs, bd.Procs...)
	}
	return c.flush(ctx, samples, disks, ios, procs)
}

func (c *Client) flush(ctx context.Context, samples []SampleRow, disks []DiskRow, ios []DiskIORow, procs []ProcRow) error {
	if len(samples) > 0 {
		// Explicit column list skips `ingested_at` (defaulted in the
		// table DDL) so the row count matches `Append` arity.
		insertSamples := `INSERT INTO ` + c.table("samples_raw") + ` (
			ts, org_id, node_id, schema_v,
			host_name, host_os, host_arch, host_kernel, host_uptime_s, host_sampler, host_machine_id,
			cpu_pct, cpu_load_1, cpu_load_5, cpu_load_15, cpu_cores,
			mem_used, mem_total, swap_used, swap_total,
			disk_read_bps, disk_write_bps, disk_read_cum, disk_write_cum,
			net_rx_bps, net_tx_bps, net_rx_cum, net_tx_cum,
			procs_total, procs_visible, procs_root,
			errors
		)`
		batch, err := c.conn.PrepareBatch(ctx, insertSamples)
		if err != nil {
			return err
		}
		for _, s := range samples {
			if err := batch.Append(
				s.TS, s.OrgID, s.NodeID, s.SchemaV,
				s.HostName, s.HostOS, s.HostArch, s.HostKernel, s.HostUptimeS, s.HostSampler, s.HostMachineID,
				s.CPUPct, s.CPULoad1, s.CPULoad5, s.CPULoad15, s.CPUCores,
				s.MemUsed, s.MemTotal, s.SwapUsed, s.SwapTotal,
				s.DiskReadBps, s.DiskWriteBps, s.DiskReadCum, s.DiskWriteCum,
				s.NetRxBps, s.NetTxBps, s.NetRxCum, s.NetTxCum,
				s.ProcsTotal, s.ProcsVisible, s.ProcsRoot,
				s.Errors,
			); err != nil {
				return err
			}
		}
		if err := batch.Send(); err != nil {
			return err
		}
	}
	if len(disks) > 0 {
		batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.table("disks"))
		if err != nil {
			return err
		}
		for _, d := range disks {
			if err := batch.Append(d.TS, d.OrgID, d.NodeID, d.Mount, d.FS, d.Used, d.Total); err != nil {
				return err
			}
		}
		if err := batch.Send(); err != nil {
			return err
		}
	}
	if len(ios) > 0 {
		batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.table("disk_io_devices"))
		if err != nil {
			return err
		}
		for _, d := range ios {
			if err := batch.Append(d.TS, d.OrgID, d.NodeID, d.Device, d.ReadBps, d.WriteBps, d.ReadCum, d.WriteCum); err != nil {
				return err
			}
		}
		if err := batch.Send(); err != nil {
			return err
		}
	}
	if len(procs) > 0 {
		batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.table("procs_topn"))
		if err != nil {
			return err
		}
		for _, p := range procs {
			if err := batch.Append(
				p.TS, p.OrgID, p.NodeID,
				p.PID, p.PPID, p.Name, p.Cmd, p.UserName,
				p.CPUPct, p.RSS, p.Threads, p.State, p.StartTS,
				p.ReadBytes, p.WriteBytes,
			); err != nil {
				return err
			}
		}
		if err := batch.Send(); err != nil {
			return err
		}
	}
	return nil
}

// WriteEvent inserts a single event row synchronously. Events are rare
// enough that batching adds no meaningful win.
func (c *Client) WriteEvent(ctx context.Context, e EventRow) error {
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO "+c.table("events"))
	if err != nil {
		return err
	}
	if err := batch.Append(
		e.TS, e.OrgID, e.NodeID, e.EventID, e.Kind, e.Metric, e.Tint, e.Payload,
		e.AckedBy, e.AckedAt, e.SnoozedUntil,
	); err != nil {
		return err
	}
	return batch.Send()
}
