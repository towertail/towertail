package wire

import (
	"math"

	"github.com/google/uuid"
	"github.com/towertail/sampler/pkg/schema"
)

// DetailTab names the part of a sample that a detail frame carries.
type DetailTab string

const (
	DetailProcesses DetailTab = "processes"
	DetailPorts     DetailTab = "ports"
	DetailDisks     DetailTab = "disks"
	DetailHealth    DetailTab = "health"
)

func (t DetailTab) Valid() bool {
	switch t {
	case DetailProcesses, DetailPorts, DetailDisks, DetailHealth:
		return true
	}
	return false
}

// ViewDetail is the host and the tab that the client has open.
type ViewDetail struct {
	Host uuid.UUID `json:"host"`
	Tab  DetailTab `json:"tab"`
}

// HostFacts are the slow facts of a host.
type HostFacts struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Kernel   string `json:"kernel,omitempty"`
	Cores    int    `json:"cores"`
	MemTotal int64  `json:"mem_total"`
	UptimeS  int64  `json:"uptime_s"`
	Sampler  string `json:"sampler"`
}

// Same reports whether the facts are equal. It ignores UptimeS, which
// changes in each sample.
func (f HostFacts) Same(o HostFacts) bool {
	f.UptimeS, o.UptimeS = 0, 0
	return f == o
}

// HealthBrief is schema.HealthInfo without ZombieParents, plus the worst
// inode ratio of all mounts.
type HealthBrief struct {
	Procs          int             `json:"procs"`
	Zombies        *int            `json:"zombies,omitempty"`
	PidsUsed       *int64          `json:"pids_used,omitempty"`
	PidsMax        *int64          `json:"pids_max,omitempty"`
	FilesUsed      *int64          `json:"files_used,omitempty"`
	FilesMax       *int64          `json:"files_max,omitempty"`
	PSI            *schema.PSIInfo `json:"psi,omitempty"`
	MemPressure    *int            `json:"mem_pressure,omitempty"`
	MemPressurePct *int            `json:"mem_pressure_pct,omitempty"`
	Inodes         float64         `json:"inodes,omitempty"`
}

// LiveSummary is the short sample of a leased host. cpu is a percent
// (0-100). mem and disk are ratios (0-1) and disk is the fullest mount.
// rx and tx are bytes per second. Host is set only in the first frame of
// a lease and after a change of the facts. Health is set only when it
// changes.
type LiveSummary struct {
	TS     string       `json:"ts"`
	CPU    float64      `json:"cpu"`
	Mem    float64      `json:"mem"`
	Disk   float64      `json:"disk"`
	Rx     int64        `json:"rx"`
	Tx     int64        `json:"tx"`
	Load   float64      `json:"load"`
	Procs  int          `json:"procs,omitempty"`
	Health *HealthBrief `json:"health,omitempty"`
	Host   *HostFacts   `json:"host,omitempty"`
}

// LiveProc is schema.ProcSample with the key of the process.
type LiveProc struct {
	schema.ProcSample
	Key ProcessKey `json:"key"`
}

// LiveProcs is schema.ProcList with a key on each process.
type LiveProcs struct {
	Root    bool       `json:"root"`
	TopN    int        `json:"top_n"`
	Total   int        `json:"total"`
	Visible int        `json:"visible"`
	Skipped bool       `json:"skipped,omitempty"`
	Items   []LiveProc `json:"items"`
}

// NewLiveProcs adds the key to each process of the list. It returns nil
// for a nil list.
func NewLiveProcs(p *Procs) *LiveProcs {
	if p == nil {
		return nil
	}
	out := &LiveProcs{
		Root: p.Root, TopN: p.TopN, Total: p.Total, Visible: p.Visible, Skipped: p.Skipped,
		Items: make([]LiveProc, len(p.Items)),
	}
	for i, it := range p.Items {
		out.Items[i] = LiveProc{ProcSample: it, Key: NewProcessKey(it.Name, it.Cmd)}
	}
	return out
}

// DetailParts holds the keys of one detail tab. The names are the same
// as in schema.Sample.
type DetailParts struct {
	TS     string             `json:"ts"`
	Procs  *LiveProcs         `json:"procs,omitempty"`
	Ports  *schema.PortList   `json:"ports,omitempty"`
	Disks  *[]Disk            `json:"disks,omitempty"`
	DiskIO *DiskIO            `json:"disk_io,omitempty"`
	Health *schema.HealthInfo `json:"health,omitempty"`
}

// StreamHello is the first frame of a lease connection.
type StreamHello struct {
	Lease   bool `json:"lease"`
	RenewS  int  `json:"renew_s"`
	MaxTTLS int  `json:"max_ttl_s"`
}

// Reasons of LeaseNotice.
const LeaseEndedLimit = "limit"

// LeaseNotice tells the client that its lease changed. State is "ended".
type LeaseNotice struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// ratio rounds to 4 decimals.
func ratio(used, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(used)/float64(total)*1e4) / 1e4
}

// Summary returns the short sample, without Host.
func (s Sample) Summary() LiveSummary {
	out := LiveSummary{
		TS:   s.TS,
		CPU:  s.CPU.Pct,
		Mem:  ratio(s.Mem.Used, s.Mem.Total),
		Load: s.CPU.Load1,
	}
	if s.Disks != nil {
		for _, d := range *s.Disks {
			out.Disk = max(out.Disk, ratio(d.Used, d.Total))
		}
	}
	if s.Net != nil {
		out.Rx, out.Tx = s.Net.RxBps, s.Net.TxBps
	}
	switch {
	case s.Procs != nil:
		out.Procs = s.Procs.Total
	case s.Health != nil:
		out.Procs = s.Health.Procs
	}
	if s.Health != nil {
		out.Health = s.healthBrief()
	}
	return out
}

func (s Sample) healthBrief() *HealthBrief {
	h := s.Health
	b := &HealthBrief{
		Procs: h.Procs, Zombies: h.Zombies,
		PidsUsed: h.PidsUsed, PidsMax: h.PidsMax,
		FilesUsed: h.FilesUsed, FilesMax: h.FilesMax,
		PSI: h.PSI, MemPressure: h.MemPressure, MemPressurePct: h.MemPressurePct,
	}
	if s.Disks != nil {
		for _, d := range *s.Disks {
			b.Inodes = max(b.Inodes, ratio(d.InodesUsed, d.InodesTotal))
		}
	}
	return b
}

func (s Sample) Facts() HostFacts {
	return HostFacts{
		OS: s.Host.OS, Arch: s.Host.Arch, Kernel: s.Host.Kernel,
		Cores: s.CPU.Cores, MemTotal: s.Mem.Total,
		UptimeS: s.Host.UptimeS, Sampler: s.Host.Sampler,
	}
}

// Detail copies the parts of the tab. It returns false when the sample
// has none of them.
func (s Sample) Detail(tab DetailTab) (DetailParts, bool) {
	d := DetailParts{TS: s.TS}
	procs := NewLiveProcs(s.Procs)
	switch tab {
	case DetailProcesses:
		d.Procs = procs
	case DetailPorts:
		d.Ports = s.Ports
	case DetailDisks:
		d.Disks, d.DiskIO, d.Procs = s.Disks, s.DiskIO, procs
	case DetailHealth:
		d.Health, d.Procs = s.Health, procs
	default:
		return d, false
	}
	ok := d.Procs != nil || d.Ports != nil || d.Disks != nil || d.DiskIO != nil || d.Health != nil
	return d, ok
}
