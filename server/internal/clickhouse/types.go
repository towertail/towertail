package clickhouse

import (
	"time"

	"github.com/google/uuid"
)

// SampleRow is the denormalised wide row written into samples_raw.
type SampleRow struct {
	TS             time.Time
	OrgID          uuid.UUID
	NodeID         uuid.UUID
	SchemaV        uint8
	HostName       string
	HostOS         string
	HostArch       string
	HostKernel     string
	HostUptimeS    uint64
	HostSampler    string
	HostMachineID  string
	CPUPct         float32
	CPULoad1       float32
	CPULoad5       float32
	CPULoad15      float32
	CPUCores       uint16
	MemUsed        uint64
	MemTotal       uint64
	SwapUsed       uint64
	SwapTotal      uint64
	DiskReadBps    uint64
	DiskWriteBps   uint64
	DiskReadCum    uint64
	DiskWriteCum   uint64
	NetRxBps       uint64
	NetTxBps       uint64
	NetRxCum       uint64
	NetTxCum       uint64
	ProcsTotal     uint32
	ProcsVisible   uint32
	ProcsRoot      uint8
	Errors         []string
}

// DiskRow is one row per mount per sample.
type DiskRow struct {
	TS     time.Time
	OrgID  uuid.UUID
	NodeID uuid.UUID
	Mount  string
	FS     string
	Used   uint64
	Total  uint64
}

// DiskIORow is one row per block device per sample.
type DiskIORow struct {
	TS       time.Time
	OrgID    uuid.UUID
	NodeID   uuid.UUID
	Device   string
	ReadBps  uint64
	WriteBps uint64
	ReadCum  uint64
	WriteCum uint64
}

// ProcRow is one row per process per sample.
type ProcRow struct {
	TS         time.Time
	OrgID      uuid.UUID
	NodeID     uuid.UUID
	PID        int32
	PPID       int32
	Name       string
	Cmd        string
	UserName   string
	CPUPct     float32
	RSS        uint64
	Threads    int32
	State      string
	StartTS    time.Time
	ReadBytes  *uint64
	WriteBytes *uint64
}

// EventRow is a persisted alert.
type EventRow struct {
	TS           time.Time
	OrgID        uuid.UUID
	NodeID       uuid.UUID
	EventID      uuid.UUID
	Kind         string
	Metric       string
	Tint         string
	Payload      string
	AckedBy      *uuid.UUID
	AckedAt      *time.Time
	SnoozedUntil *time.Time
}

// IngestBundle carries every row set derived from one Sample. Ingest
// handlers build this once and hand it off to the batcher.
type IngestBundle struct {
	Sample  SampleRow
	Disks   []DiskRow
	DiskIO  []DiskIORow
	Procs   []ProcRow
}
