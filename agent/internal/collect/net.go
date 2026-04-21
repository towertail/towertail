package collect

import (
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/net"
	"github.com/towertail/agent/internal/schema"
)

func isIncludedIface(name string) bool {
	n := strings.ToLower(name)
	if n == "" {
		return false
	}
	prefixes := []string{
		"lo",     // loopback
		"docker", // docker bridge
		"veth",   // container veth pair
		"br-",    // docker user-bridges
		"utun",   // macOS tunnels
		"llw",    // macOS low-latency Wi-Fi awdl peer
		"awdl",   // Apple Wireless Direct Link
		"bridge", // macOS bridge
		"gif",    // generic tunnel
		"stf",    // 6to4 tunnel
		"anpi",   // macOS internal
		"ap",     // macOS access point virtual
		"vmenet", // macOS VM networking
	}
	for _, p := range prefixes {
		if strings.HasPrefix(n, p) {
			return false
		}
	}
	return true
}

func sumCounters(stats []net.IOCountersStat) (rx, tx uint64) {
	for _, s := range stats {
		if !isIncludedIface(s.Name) {
			continue
		}
		rx += s.BytesRecv
		tx += s.BytesSent
	}
	return
}

// Net returns the aggregate rx/tx counters and bps deltas across
// non-loopback, non-virtual interfaces. Self-samples over the given window.
func Net(window time.Duration) (schema.NetInfo, []string) {
	var errs []string
	var n schema.NetInfo

	first, err := net.IOCounters(false)
	if err != nil {
		errs = append(errs, "net.IOCounters: "+err.Error())
		return n, errs
	}
	// IOCounters(false) returns a single aggregate row on some platforms;
	// request per-interface so we can filter virtual ones.
	firstPer, err := net.IOCounters(true)
	if err != nil {
		errs = append(errs, "net.IOCounters(per): "+err.Error())
		return n, errs
	}
	_ = first

	rx0, tx0 := sumCounters(firstPer)
	time.Sleep(window)

	secondPer, err := net.IOCounters(true)
	if err != nil {
		errs = append(errs, "net.IOCounters(per2): "+err.Error())
		return n, errs
	}
	rx1, tx1 := sumCounters(secondPer)

	var rxDelta, txDelta uint64
	if rx1 >= rx0 {
		rxDelta = rx1 - rx0
	}
	if tx1 >= tx0 {
		txDelta = tx1 - tx0
	}
	secs := window.Seconds()
	if secs <= 0 {
		secs = 1
	}
	n.RxBps = int64(float64(rxDelta) / secs)
	n.TxBps = int64(float64(txDelta) / secs)
	n.RxCum = int64(rx1)
	n.TxCum = int64(tx1)
	return n, errs
}
