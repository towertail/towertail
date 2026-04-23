package api

import (
	"testing"

	"github.com/google/uuid"
)

func TestDecodeSample(t *testing.T) {
	line := []byte(`{
		"v": 1,
		"ts": "2026-04-23T10:00:00.000Z",
		"host": {"name":"x","os":"linux","arch":"amd64","kernel":"6.6","uptime_s":1,"sampler":"0.1.0"},
		"cpu": {"pct":12.5,"load_1":0.1,"load_5":0.2,"load_15":0.3,"cores":4},
		"mem": {"used":1,"total":100},
		"swap": {"used":0,"total":0},
		"disk_io": {"read_bps":1,"write_bps":2,"read_cum":10,"write_cum":20},
		"errors": []
	}`)
	org, node := uuid.New(), uuid.New()
	bundle, err := decodeSample(line, org, node)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bundle.Sample.HostName != "x" {
		t.Errorf("host: %s", bundle.Sample.HostName)
	}
	if bundle.Sample.CPUPct != 12.5 {
		t.Errorf("cpu: %v", bundle.Sample.CPUPct)
	}
	if bundle.Sample.DiskWriteBps != 2 {
		t.Errorf("disk io: %v", bundle.Sample.DiskWriteBps)
	}
}

func TestDecodeSample_RejectsUnknownVersion(t *testing.T) {
	line := []byte(`{"v":99,"ts":"2026-04-23T10:00:00Z","host":{"name":"x"},"cpu":{},"mem":{"used":1,"total":100},"swap":{"used":0,"total":0},"errors":[]}`)
	if _, err := decodeSample(line, uuid.New(), uuid.New()); err == nil {
		t.Error("expected error")
	}
}
