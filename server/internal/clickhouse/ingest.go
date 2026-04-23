package clickhouse

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// batcher aggregates IngestBundles and flushes them in grouped batches.
// One writer goroutine per shard (v1: a single shard is plenty at
// 10k-host scale). Flushes on row count, byte estimate, or period.
type batcher struct {
	client *Client
	log    *slog.Logger

	in     chan IngestBundle
	wg     sync.WaitGroup
	stopCh chan struct{}
}

func newBatcher(c *Client, log *slog.Logger) *batcher {
	return &batcher{
		client: c,
		log:    log,
		in:     make(chan IngestBundle, 4096),
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

// Enqueue submits a bundle; returns false if the batcher is stopping or
// its queue is full (caller can 503 or retry).
func (b *batcher) Enqueue(bundle IngestBundle) bool {
	select {
	case b.in <- bundle:
		return true
	default:
		return false
	}
}

func (b *batcher) run() {
	defer b.wg.Done()
	cfg := b.client.cfg
	period := cfg.BatchFlushPeriod
	if period <= 0 {
		period = time.Second
	}
	maxRows := cfg.BatchFlushRows
	if maxRows <= 0 {
		maxRows = 5000
	}
	maxBytes := cfg.BatchFlushBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}

	var (
		samples []SampleRow
		disks   []DiskRow
		ios     []DiskIORow
		procs   []ProcRow
		bytes   int
	)
	tick := time.NewTicker(period)
	defer tick.Stop()

	flush := func(reason string) {
		if len(samples) == 0 && len(disks) == 0 && len(ios) == 0 && len(procs) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := b.flush(ctx, samples, disks, ios, procs); err != nil {
			b.log.Error("clickhouse batcher: flush failed", "reason", reason, "err", err)
		} else {
			b.log.Debug("clickhouse batcher: flushed",
				"reason", reason,
				"samples", len(samples),
				"disks", len(disks),
				"disk_io", len(ios),
				"procs", len(procs),
			)
		}
		samples = samples[:0]
		disks = disks[:0]
		ios = ios[:0]
		procs = procs[:0]
		bytes = 0
	}

	for {
		select {
		case <-b.stopCh:
			flush("stop")
			return
		case bundle := <-b.in:
			samples = append(samples, bundle.Sample)
			disks = append(disks, bundle.Disks...)
			ios = append(ios, bundle.DiskIO...)
			procs = append(procs, bundle.Procs...)
			// Rough byte estimate, avoids JSON-marshal overhead in hot path.
			bytes += 256 + 64*len(bundle.Disks) + 80*len(bundle.DiskIO) + 256*len(bundle.Procs)
			if len(samples) >= maxRows || bytes >= maxBytes {
				flush("size")
			}
		case <-tick.C:
			flush("tick")
		}
	}
}

func (b *batcher) flush(ctx context.Context, samples []SampleRow, disks []DiskRow, ios []DiskIORow, procs []ProcRow) error {
	if len(samples) > 0 {
		// Explicit column list skips `ingested_at` (defaulted in the
		// table DDL) so the row count matches `Append` arity.
		const insertSamples = `INSERT INTO towertail.samples_raw (
			ts, org_id, node_id, schema_v,
			host_name, host_os, host_arch, host_kernel, host_uptime_s, host_sampler, host_machine_id,
			cpu_pct, cpu_load_1, cpu_load_5, cpu_load_15, cpu_cores,
			mem_used, mem_total, swap_used, swap_total,
			disk_read_bps, disk_write_bps, disk_read_cum, disk_write_cum,
			net_rx_bps, net_tx_bps, net_rx_cum, net_tx_cum,
			procs_total, procs_visible, procs_root,
			errors
		)`
		batch, err := b.client.conn.PrepareBatch(ctx, insertSamples)
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
		batch, err := b.client.conn.PrepareBatch(ctx, "INSERT INTO towertail.disks")
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
		batch, err := b.client.conn.PrepareBatch(ctx, "INSERT INTO towertail.disk_io_devices")
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
		batch, err := b.client.conn.PrepareBatch(ctx, "INSERT INTO towertail.procs_topn")
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
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO towertail.events")
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
