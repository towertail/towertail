package channels

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var sendsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "towertail_notify_sends_total",
	Help: "Alert sends to channels, after the retries. result is ok or error.",
}, []string{"kind", "result"})
