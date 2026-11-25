package mcp

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/towertail/sampler/pkg/schema"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/internal/scan"
	"github.com/towertail/towertail/server/store"
)

// DisksIn is the input of disks.
type DisksIn struct {
	Host  string `json:"host" jsonschema:"host ID or name"`
	Range string `json:"range,omitempty" jsonschema:"span up to now, 15m to 365d, for example 24h or 7d. Default 24h."`
}

// Mount is the use of one mount.
type Mount struct {
	Mount      string  `json:"mount"`
	FS         string  `json:"fs"`
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Ratio      float64 `json:"ratio"`
	// ChangeBytes is the used bytes now minus the used bytes at the start of the range.
	ChangeBytes int64 `json:"change_bytes"`
}

// DisksOut is the output of disks.
type DisksOut struct {
	Host    string  `json:"host"`
	HostID  string  `json:"host_id"`
	From    string  `json:"from"`
	To      string  `json:"to"`
	Clamped bool    `json:"clamped,omitempty"`
	Mounts  []Mount `json:"mounts"`
}

func (s *Server) disks(ctx context.Context, _ *sdk.CallToolRequest, in DisksIn) (*sdk.CallToolResult, DisksOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, DisksOut{}, err
	}
	orgID := c.Token.OrgID
	from, to, err := parseRange(cmp.Or(in.Range, "24h"), "", "", s.now().UTC())
	if err != nil {
		return nil, DisksOut{}, err
	}
	if strings.TrimSpace(in.Host) == "" {
		return nil, DisksOut{}, badf("host is required")
	}
	node, err := s.q.ResolveHost(ctx, orgID, in.Host)
	if err != nil {
		return nil, DisksOut{}, s.toolError("disks", err)
	}
	res, err := s.q.Disks(ctx, orgID, node.ID, from, to)
	if err != nil {
		return nil, DisksOut{}, s.toolError("disks", err)
	}
	out := DisksOut{Host: node.DisplayName, HostID: node.ID.String(), From: ts(res.From), To: ts(res.To), Clamped: res.Clamped, Mounts: []Mount{}}
	// The points come in mount order, the oldest first.
	var first clickhouse.DiskPoint
	for i, p := range res.Points {
		if i == 0 || p.Mount != res.Points[i-1].Mount {
			first = p
			out.Mounts = append(out.Mounts, Mount{Mount: p.Mount})
		}
		m := &out.Mounts[len(out.Mounts)-1]
		m.FS, m.UsedBytes, m.TotalBytes = p.FS, p.Used, p.Total
		m.ChangeBytes = int64(p.Used) - int64(first.Used)
		m.Ratio = 0
		if p.Total > 0 {
			m.Ratio = float64(p.Used) / float64(p.Total)
		}
	}
	return nil, out, nil
}

// Limits of disk_scans.
const (
	maxScanJobs = 5
	maxScanTop  = 20
)

// DiskScansIn is the input of disk_scans.
type DiskScansIn struct {
	Host  string `json:"host" jsonschema:"host ID or name"`
	JobID string `json:"job_id,omitempty" jsonschema:"scan job ID, from an earlier call. Default: the newest finished scan."`
}

// ScanJobOut is one disk scan job.
type ScanJobOut struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Root        string `json:"root"`
	Created     string `json:"created"`
	Finished    string `json:"finished,omitempty"`
	Error       string `json:"error,omitempty"`
	ResultBytes int64  `json:"result_bytes"`
}

// ScanFSOut is the file system that holds the root of a scan.
type ScanFSOut struct {
	Mount string `json:"mount"`
	Total int64  `json:"total"`
	Used  int64  `json:"used"`
	Free  int64  `json:"free"`
}

// ScanPath is one path of a scan and its size.
type ScanPath struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// ScanRecOut is a cleanup recommendation of a scan.
type ScanRecOut struct {
	Group    string `json:"group"`
	Label    string `json:"label"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Elevated bool   `json:"elevated,omitempty"`
}

// ScanResultOut is the result of a finished scan. Top lists the 20 largest
// entries (files, or folders that the scan did not open further), the
// largest first. Sizes are allocated bytes.
type ScanResultOut struct {
	JobID        string       `json:"job_id"`
	Root         string       `json:"root"`
	FS           *ScanFSOut   `json:"fs,omitempty"`
	ScannedBytes int64        `json:"scanned_bytes"`
	Top          []ScanPath   `json:"top"`
	Recs         []ScanRecOut `json:"recs"`
}

// DiskScansOut is the output of disk_scans. Jobs are the newest 5, the
// newest first.
type DiskScansOut struct {
	Host   string         `json:"host"`
	HostID string         `json:"host_id"`
	Jobs   []ScanJobOut   `json:"jobs"`
	Result *ScanResultOut `json:"result,omitempty"`
}

func newScanJob(j scan.Job) ScanJobOut {
	out := ScanJobOut{
		ID: j.ID.String(), State: string(j.State), Root: j.Root, Created: ts(j.CreatedTS), Finished: tsPtr(j.FinishedTS),
		Error: j.Error, ResultBytes: j.ResultBytes,
	}
	return out
}

var errScanNotFound = errors.New("disk scan not found")

func (s *Server) diskScans(ctx context.Context, _ *sdk.CallToolRequest, in DiskScansIn) (*sdk.CallToolResult, DiskScansOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, DiskScansOut{}, err
	}
	orgID := c.Token.OrgID
	if s.scans == nil {
		return nil, DiskScansOut{}, badf("disk scans are off")
	}
	if strings.TrimSpace(in.Host) == "" {
		return nil, DiskScansOut{}, badf("host is required")
	}
	var want uuid.UUID
	if in.JobID != "" {
		if want, err = uuid.Parse(strings.TrimSpace(in.JobID)); err != nil {
			return nil, DiskScansOut{}, badf("job_id must be a scan job ID")
		}
	}
	node, err := s.q.ResolveHost(ctx, orgID, in.Host)
	if err != nil {
		return nil, DiskScansOut{}, s.toolError("disk_scans", err)
	}
	jobs, err := s.scans.List(ctx, orgID, node.ID)
	if err != nil {
		return nil, DiskScansOut{}, s.toolError("disk_scans", err)
	}
	out := DiskScansOut{Host: node.DisplayName, HostID: node.ID.String(), Jobs: []ScanJobOut{}}
	for _, j := range jobs[:min(len(jobs), maxScanJobs)] {
		out.Jobs = append(out.Jobs, newScanJob(j))
	}
	var job *scan.Job
	if want != uuid.Nil {
		j, err := s.scans.Get(ctx, want)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (j.OrgID != orgID || j.NodeID != node.ID)) {
			return nil, DiskScansOut{}, errScanNotFound
		}
		if err != nil {
			return nil, DiskScansOut{}, s.toolError("disk_scans", err)
		}
		if j.State != scan.StateDone {
			return nil, DiskScansOut{}, badf("disk scan %s is not done: its state is %s", j.ID, j.State)
		}
		job = &j
	} else if i := slices.IndexFunc(jobs, func(j scan.Job) bool { return j.State == scan.StateDone }); i >= 0 {
		job = &jobs[i]
	}
	if job == nil {
		return nil, out, nil
	}
	gz, err := s.scans.Result(ctx, job.ID)
	if err != nil {
		return nil, DiskScansOut{}, s.toolError("disk_scans", err)
	}
	res, err := readScanResult(gz)
	if err != nil {
		return nil, DiskScansOut{}, s.toolError("disk_scans", err)
	}
	out.Result = newScanResult(job.ID, res)
	return nil, out, nil
}

// readScanResult decodes the gzip body of a scan result.
func readScanResult(gz []byte) (schema.ScanResult, error) {
	var r schema.ScanResult
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return r, err
	}
	defer zr.Close()
	lr := &io.LimitedReader{R: zr, N: scan.MaxResultJSONBytes + 1}
	if err := json.NewDecoder(lr).Decode(&r); err != nil {
		return r, err
	}
	if lr.N <= 0 {
		return r, scan.ErrTooLarge
	}
	return r, nil
}

func newScanResult(id uuid.UUID, r schema.ScanResult) *ScanResultOut {
	out := &ScanResultOut{JobID: id.String(), Root: r.Root, ScannedBytes: r.ScannedBytes, Top: scanTop(r), Recs: []ScanRecOut{}}
	if f := r.FS; f != nil {
		out.FS = &ScanFSOut{Mount: f.Mount, Total: f.Total, Used: f.Used, Free: f.Free}
	}
	for _, rec := range r.Recs {
		out.Recs = append(out.Recs, ScanRecOut{Group: rec.Group, Label: rec.Label, Path: rec.Path, Bytes: rec.Bytes, Elevated: rec.Elevated})
	}
	return out
}

// scanTop gives the largest nodes of the pruned tree that have no children
// in the tree. The node that holds the small files of a folder is left out.
func scanTop(r schema.ScanResult) []ScanPath {
	sep := "/"
	if r.OS == "windows" {
		sep = `\`
	}
	var all []ScanPath
	var walk func(n schema.ScanNode, path string)
	walk = func(n schema.ScanNode, path string) {
		if len(n.C) == 0 {
			if !n.O {
				all = append(all, ScanPath{Path: path, Bytes: n.B})
			}
			return
		}
		for _, c := range n.C {
			walk(c, strings.TrimSuffix(path, sep)+sep+c.N)
		}
	}
	walk(r.Tree, r.Tree.N)
	slices.SortStableFunc(all, func(a, b ScanPath) int { return cmp.Compare(b.Bytes, a.Bytes) })
	return all[:min(len(all), maxScanTop)]
}
