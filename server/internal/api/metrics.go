package api

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	ingestLines = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "towertail_ingest_lines_total",
		Help: "Ingest NDJSON lines, by result (accepted, rejected, busy).",
	}, []string{"result"})
	ingestRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "towertail_ingest_requests_total",
		Help: "Ingest requests, by HTTP status code.",
	}, []string{"code"})
)
