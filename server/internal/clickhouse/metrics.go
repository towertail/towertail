package clickhouse

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	batcherQueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "towertail_batcher_queue_depth",
		Help: "Bundles in the ClickHouse batcher queue.",
	})
	batcherFlushSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "towertail_batcher_flush_seconds",
		Help:    "Time of a good batcher flush.",
		Buckets: prometheus.ExponentialBuckets(0.005, 2, 12),
	})
	batcherFlushRows = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "towertail_batcher_flush_rows",
		Help:    "Rows in a good batcher flush.",
		Buckets: prometheus.ExponentialBuckets(1, 4, 10),
	})
	batcherFlushErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "towertail_batcher_flush_errors_total",
		Help: "Batcher flush attempts that failed.",
	})
	batcherSpillBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "towertail_batcher_spill_bytes",
		Help: "Bytes in the spill folder.",
	})
)
