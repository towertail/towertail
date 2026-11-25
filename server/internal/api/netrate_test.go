package api

import (
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

var netT0 = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

func netAt(r *netRates, node uuid.UUID, sec int, rx, tx int64) wire.Net {
	n := wire.Net{RxCum: rx, TxCum: tx}
	r.fill(node, netT0.Add(time.Duration(sec)*time.Second), &n)
	return n
}

func TestNetRatesFirstSampleIsZero(t *testing.T) {
	n := netAt(newNetRates(), uuid.New(), 0, 1000, 2000)
	if n.RxBps != 0 || n.TxBps != 0 {
		t.Fatalf("rates: %d %d", n.RxBps, n.TxBps)
	}
}

func TestNetRatesFromDelta(t *testing.T) {
	r, node := newNetRates(), uuid.New()
	netAt(r, node, 0, 1_000_000, 0)
	n := netAt(r, node, 10, 11_000_000, 5_000)
	if n.RxBps != 1_000_000 || n.TxBps != 500 {
		t.Fatalf("rates: %d %d", n.RxBps, n.TxBps)
	}
}

func TestNetRatesCounterReset(t *testing.T) {
	r, node := newNetRates(), uuid.New()
	netAt(r, node, 0, 5000, 5000)
	if n := netAt(r, node, 30, 100, 100); n.RxBps != 0 || n.TxBps != 0 {
		t.Fatalf("reset rates: %d %d", n.RxBps, n.TxBps)
	}
	if n := netAt(r, node, 60, 3100, 100); n.RxBps != 100 {
		t.Fatalf("after reset: %d", n.RxBps)
	}
}

func TestNetRatesGapTooLong(t *testing.T) {
	r, node := newNetRates(), uuid.New()
	netAt(r, node, 0, 0, 0)
	if n := netAt(r, node, 301, 1000, 1000); n.RxBps != 0 {
		t.Fatalf("gap rate: %d", n.RxBps)
	}
}

func TestNetRatesTimeNotForward(t *testing.T) {
	r, node := newNetRates(), uuid.New()
	netAt(r, node, 10, 0, 0)
	if n := netAt(r, node, 10, 1000, 1000); n.RxBps != 0 {
		t.Fatalf("same ts rate: %d", n.RxBps)
	}
}

func TestNetRatesKeepsSamplerRates(t *testing.T) {
	r, node := newNetRates(), uuid.New()
	netAt(r, node, 0, 0, 0)
	n := wire.Net{RxBps: 7, TxBps: 9, RxCum: 1000, TxCum: 1000}
	r.fill(node, netT0.Add(10*time.Second), &n)
	if n.RxBps != 7 || n.TxBps != 9 {
		t.Fatalf("rates: %d %d", n.RxBps, n.TxBps)
	}
}

func TestNetRatesPerNode(t *testing.T) {
	r, a, b := newNetRates(), uuid.New(), uuid.New()
	netAt(r, a, 0, 0, 0)
	if n := netAt(r, b, 10, 1000, 1000); n.RxBps != 0 {
		t.Fatalf("node b took node a counters: %d", n.RxBps)
	}
}

func TestNetRatesApplyFillsRow(t *testing.T) {
	r, org, node := newNetRates(), uuid.New(), uuid.New()
	line := func(ts string, rx int64) []byte {
		return []byte(`{"v":1,"ts":"` + ts + `","host":{"name":"x","os":"linux","arch":"amd64","kernel":"6.6","uptime_s":1,"sampler":"1.5.0"},"cpu":{"pct":1,"load_1":0,"load_5":0,"load_15":0,"cores":2},"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},"net":{"rx_bps":0,"tx_bps":0,"rx_cum":` + strconv.FormatInt(rx, 10) + `,"tx_cum":0},"errors":[]}`)
	}
	for i, l := range [][]byte{line("2026-11-25T10:00:00.000Z", 0), line("2026-11-25T10:00:30.000Z", 3_000_000)} {
		b, s, err := decodeSample(l, org, node)
		if err != nil {
			t.Fatal(err)
		}
		r.apply(node, &b, &s)
		if i == 1 && (b.Sample.NetRxBps != 100_000 || s.Net.RxBps != 100_000) {
			t.Fatalf("row %d, sample %d", b.Sample.NetRxBps, s.Net.RxBps)
		}
	}
}

func TestNetRatesApplyNoNet(t *testing.T) {
	b, s, err := decodeSample([]byte(sampleLine), uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	newNetRates().apply(uuid.New(), &b, &s)
	if b.Sample.NetRxBps != 0 {
		t.Fatalf("rx: %d", b.Sample.NetRxBps)
	}
}
