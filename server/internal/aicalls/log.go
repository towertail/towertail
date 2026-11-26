// Package aicalls buffers the rows of the AI call log and writes them to
// ClickHouse in batches.
package aicalls

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/towertail/towertail/server/internal/clickhouse"
)

const (
	bufferSize    = 4096
	batchSize     = 500
	flushInterval = 2 * time.Second
	writeTimeout  = 10 * time.Second
)

var dropped = promauto.NewCounter(prometheus.CounterOpts{
	Name: "towertail_ai_calls_dropped_total",
	Help: "AI call rows dropped because the buffer was full or the write failed.",
})

// Writer stores a batch of rows.
type Writer interface {
	WriteAICalls(ctx context.Context, rows []clickhouse.AICall) error
}

// Log buffers rows and writes them in the background.
type Log struct {
	w     Writer
	log   *slog.Logger
	rows  chan clickhouse.AICall
	every time.Duration
}

// NewLog makes the log. Run writes the rows.
func NewLog(w Writer, log *slog.Logger) *Log {
	return &Log{w: w, log: log, rows: make(chan clickhouse.AICall, bufferSize), every: flushInterval}
}

// Record adds a row. It does not block. A full buffer drops the row. A nil
// Log does nothing.
func (l *Log) Record(c clickhouse.AICall) {
	if l == nil {
		return
	}
	select {
	case l.rows <- c:
	default:
		dropped.Inc()
	}
}

// Run writes the rows each 2 s, or at 500 rows, until ctx ends. Then it
// writes the rows that are left.
func (l *Log) Run(ctx context.Context) {
	tick := time.NewTicker(l.every)
	defer tick.Stop()
	batch := make([]clickhouse.AICall, 0, batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		l.write(batch)
		batch = batch[:0]
	}
	for {
		select {
		case r := <-l.rows:
			batch = append(batch, r)
			if len(batch) >= batchSize {
				flush()
			}
		case <-tick.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case r := <-l.rows:
					batch = append(batch, r)
					if len(batch) >= batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// write stores one batch. A failed batch is dropped.
func (l *Log) write(rows []clickhouse.AICall) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := l.w.WriteAICalls(ctx, rows); err != nil {
		dropped.Add(float64(len(rows)))
		l.log.Warn("ai calls: write failed", "rows", len(rows), "err", err)
	}
}
