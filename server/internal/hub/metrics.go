package hub

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	streamClients = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "towertail_stream_clients",
		Help: "Connected stream clients, by mode (legacy, lease).",
	}, []string{"mode"})
	streamLeasesActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "towertail_stream_leases_active",
		Help: "Leases that a client holds. An expired lease leaves the count at the next sample of the org or the next view frame.",
	})
	streamLeaseEnds = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "towertail_stream_lease_ends_total",
		Help: "Leases that the server ended, by reason (limit).",
	}, []string{"reason"})
	streamFrames = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "towertail_stream_frames_total",
		Help: "Frames queued for stream clients, by frame type.",
	}, []string{"type"})
	streamFrameBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "towertail_stream_frame_bytes_total",
		Help: "Bytes of the frames queued for stream clients, by frame type.",
	}, []string{"type"})
)

func modeLabel(lease bool) string {
	if lease {
		return "lease"
	}
	return "legacy"
}
