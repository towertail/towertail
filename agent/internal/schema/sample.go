package schema

import "time"

const SchemaVersion = 1

const tsLayout = "2006-01-02T15:04:05.000Z"

type Sample struct {
	V      int           `json:"v"`
	TS     string        `json:"ts"`
	Host   HostInfo      `json:"host"`
	CPU    CPUInfo       `json:"cpu"`
	Mem    MemInfo       `json:"mem"`
	Swap   MemInfo       `json:"swap"`
	Disks  *[]DiskSample `json:"disks,omitempty"`
	Net    *NetInfo      `json:"net,omitempty"`
	Errors []string      `json:"errors"`
}

type HostInfo struct {
	Name      string `json:"name"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Kernel    string `json:"kernel"`
	UptimeS   int64  `json:"uptime_s"`
	Agent     string `json:"agent"`
	MachineID string `json:"machine_id,omitempty"`
}

type CPUInfo struct {
	Pct    float64 `json:"pct"`
	Load1  float64 `json:"load_1"`
	Load5  float64 `json:"load_5"`
	Load15 float64 `json:"load_15"`
	Cores  int     `json:"cores"`
}

type MemInfo struct {
	Used  int64 `json:"used"`
	Total int64 `json:"total"`
}

type DiskSample struct {
	Mount string `json:"mount"`
	Fs    string `json:"fs"`
	Used  int64  `json:"used"`
	Total int64  `json:"total"`
}

type NetInfo struct {
	RxBps int64 `json:"rx_bps"`
	TxBps int64 `json:"tx_bps"`
	RxCum int64 `json:"rx_cum"`
	TxCum int64 `json:"tx_cum"`
}

func FormatTS(t time.Time) string {
	return t.UTC().Format(tsLayout)
}
