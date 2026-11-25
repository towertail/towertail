package clickhouse

import (
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/towertail/sampler/pkg/schema"
)

// SampleRow is the denormalised wide row written into samples_raw.
type SampleRow struct {
	TS            time.Time
	OrgID         uuid.UUID
	NodeID        uuid.UUID
	SchemaV       uint8
	HostName      string
	HostOS        string
	HostArch      string
	HostKernel    string
	HostUptimeS   uint64
	HostSampler   string
	HostMachineID string
	CPUPct        float32
	CPULoad1      float32
	CPULoad5      float32
	CPULoad15     float32
	CPUCores      uint16
	MemUsed       uint64
	MemTotal      uint64
	SwapUsed      uint64
	SwapTotal     uint64
	DiskReadBps   uint64
	DiskWriteBps  uint64
	DiskReadCum   uint64
	DiskWriteCum  uint64
	NetRxBps      uint64
	NetTxBps      uint64
	NetRxCum      uint64
	NetTxCum      uint64
	ProcsTotal    uint32
	ProcsVisible  uint32
	ProcsRoot     uint8
	Errors        []string

	// Health values. They are nil when the sampler or the OS gives none.
	Zombies                                *uint32
	PidsUsed, PidsMax, FilesUsed, FilesMax *uint64
	PSICPUSome, PSIMemSome, PSIMemFull     *float32
	PSIIOSome, PSIIOFull                   *float32
	MemPressurePct                         *uint8
	DiskRatioMax, InodesRatioMax           *float32
}

// SetHealth copies the health values of a sample. A value that the sample
// does not have stays nil.
func (s *SampleRow) SetHealth(h *schema.HealthInfo) {
	if h.Zombies != nil {
		s.Zombies = ptr(uint32Positive(int64(*h.Zombies)))
	}
	s.PidsUsed, s.PidsMax = positive(h.PidsUsed), positive(h.PidsMax)
	s.FilesUsed, s.FilesMax = positive(h.FilesUsed), positive(h.FilesMax)
	if p := h.PSI; p != nil {
		s.PSICPUSome, s.PSIMemSome, s.PSIMemFull = ptr(float32(p.CPUSome)), ptr(float32(p.MemSome)), ptr(float32(p.MemFull))
		s.PSIIOSome, s.PSIIOFull = ptr(float32(p.IOSome)), ptr(float32(p.IOFull))
	}
	if h.MemPressurePct != nil {
		s.MemPressurePct = ptr(uint8(min(max(*h.MemPressurePct, 0), 100)))
	}
}

func ptr[T any](v T) *T { return &v }

func uint32Positive(v int64) uint32 {
	return uint32(min(max(v, 0), math.MaxUint32))
}

// positive converts a count; a negative value is 0 and a nil stays nil.
func positive(v *int64) *uint64 {
	if v == nil {
		return nil
	}
	return ptr(uint64(max(*v, 0)))
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
	// InodesUsed and InodesTotal are nil when the filesystem has no inodes.
	InodesUsed  *uint64
	InodesTotal *uint64
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
	Cwd        string
	Repo       string
	Parent     string
	ReadBps    *uint64
	WriteBps   *uint64
}

// PortRow is the port footprint of one process in one ports snapshot. A
// row exists only for a process with a listening port.
type PortRow struct {
	TS        time.Time
	OrgID     uuid.UUID
	NodeID    uuid.UUID
	PID       int32
	Name      string
	User      string
	ListenTCP []uint16
	ListenUDP []uint16
	EstIn     uint32
	EstOut    uint32
}

// EventRow is a persisted alert.
type EventRow struct {
	TS      time.Time
	OrgID   uuid.UUID
	NodeID  uuid.UUID
	EventID uuid.UUID
	// AlertID is the id of the alert that the event belongs to. A zero
	// value writes EventID.
	AlertID      uuid.UUID
	Kind         string
	Metric       string
	Tint         string
	Payload      string
	AckedBy      *uuid.UUID
	AckedAt      *time.Time
	SnoozedUntil *time.Time
}

// AlertLine is one append-only line of the alert log.
type AlertLine struct {
	TS      time.Time
	OrgID   uuid.UUID
	NodeID  uuid.UUID
	AlertID uuid.UUID
	Kind    string
	User    *uuid.UUID
	Until   *time.Time
	Text    string
}

// AlertMark is the last ack state and the last snooze state of an alert.
// AckedBy is nil after an unack. SnoozedUntil is nil after an unsnooze.
type AlertMark struct {
	OrgID        uuid.UUID
	AlertID      uuid.UUID
	AckedBy      *uuid.UUID
	AckedAt      *time.Time
	SnoozedUntil *time.Time
	SnoozedBy    *uuid.UUID
}

// IngestBundle carries every row set derived from one Sample. Ingest
// handlers build this once and hand it off to the batcher.
type IngestBundle struct {
	Sample SampleRow
	Disks  []DiskRow
	DiskIO []DiskIORow
	Procs  []ProcRow
	Ports  []PortRow
}
