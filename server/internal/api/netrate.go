package api

import (
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/wire"
)

// netRateMaxGap is the longest time between two samples that gives a rate.
const netRateMaxGap = 5 * time.Minute

// netRates fills the net rates of push samples. The sampler sends only the
// counters, so the server takes the delta to the last sample of the node.
type netRates struct {
	mu   sync.Mutex
	last map[uuid.UUID]netCount
}

type netCount struct {
	ts     time.Time
	rx, tx int64
}

func newNetRates() *netRates {
	return &netRates{last: map[uuid.UUID]netCount{}}
}

// fill sets RxBps and TxBps of n when the sampler sent no rate. The first
// sample, a long gap, a time that does not go forward and a counter reset
// give 0.
func (r *netRates) fill(nodeID uuid.UUID, ts time.Time, n *wire.Net) {
	r.mu.Lock()
	prev, ok := r.last[nodeID]
	r.last[nodeID] = netCount{ts: ts, rx: n.RxCum, tx: n.TxCum}
	r.mu.Unlock()

	if n.RxBps != 0 || n.TxBps != 0 || !ok {
		return
	}
	dt := ts.Sub(prev.ts)
	if dt <= 0 || dt > netRateMaxGap || n.RxCum < prev.rx || n.TxCum < prev.tx {
		return
	}
	secs := dt.Seconds()
	n.RxBps = int64(float64(n.RxCum-prev.rx) / secs)
	n.TxBps = int64(float64(n.TxCum-prev.tx) / secs)
}

// apply fills the net rates of the sample and of its ClickHouse row.
func (r *netRates) apply(nodeID uuid.UUID, b *clickhouse.IngestBundle, s *wire.Sample) {
	if s.Net == nil {
		return
	}
	r.fill(nodeID, b.Sample.TS, s.Net)
	b.Sample.NetRxBps = uint64Positive(s.Net.RxBps)
	b.Sample.NetTxBps = uint64Positive(s.Net.TxBps)
}
