package mcp

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/towertail/sampler/pkg/schema"

	"github.com/towertail/towertail/server/internal/scan"
	"github.com/towertail/towertail/server/store"
)

type fakeScans struct {
	jobs    []scan.Job
	results map[uuid.UUID][]byte
}

func (f *fakeScans) List(_ context.Context, org, node uuid.UUID) ([]scan.Job, error) {
	var out []scan.Job
	for _, j := range f.jobs {
		if j.OrgID == org && j.NodeID == node {
			out = append(out, j)
		}
	}
	return out, nil
}

func (f *fakeScans) Get(_ context.Context, id uuid.UUID) (scan.Job, error) {
	for _, j := range f.jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return scan.Job{}, store.ErrNotFound
}

func (f *fakeScans) Result(_ context.Context, id uuid.UUID) ([]byte, error) {
	return f.results[id], nil
}

// scanFixture gives a gzip result with 25 files below /var, a small-files
// node and one folder with children.
func scanFixture(t *testing.T) []byte {
	t.Helper()
	var files []schema.ScanNode
	for i := 1; i <= 25; i++ {
		files = append(files, schema.ScanNode{N: fmt.Sprintf("f%02d", i), B: int64(i * 10), F: true})
	}
	files = append(files, schema.ScanNode{N: "(small)", B: 5000, O: true})
	res := schema.ScanResult{
		T: schema.ScanLineResult, V: schema.ScanVersion, Root: "/", OS: "linux", ScannedBytes: 4000,
		FS:   &schema.ScanFS{Mount: "/", Total: 100, Used: 60, Free: 40},
		Tree: schema.ScanNode{N: "/", B: 4000, C: []schema.ScanNode{{N: "var", B: 3000, C: files}, {N: "tmp", B: 15}}},
		Recs: []schema.ScanRec{{ID: "r1", Group: "cache", Label: "Old logs", Path: "/var/log", Bytes: 99, Elevated: true}},
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(res); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDiskScansTool(t *testing.T) {
	f := newFake()
	node := f.nodes[0].ID
	var jobs []scan.Job
	done := uuid.New()
	// The store gives the newest job first.
	for i := range 7 {
		j := scan.Job{ID: uuid.New(), OrgID: orgA, NodeID: node, State: scan.StateFailed, Root: "/", CreatedTS: now.Add(-time.Duration(i) * time.Hour), Error: "x"}
		if i == 0 {
			j.State, j.Error = scan.StateRunning, ""
		}
		if i == 2 {
			j.ID, j.State, j.Error = done, scan.StateDone, ""
			fin := j.CreatedTS.Add(time.Minute)
			j.FinishedTS, j.ResultBytes = &fin, 1234
		}
		jobs = append(jobs, j)
	}
	other := scan.Job{ID: uuid.New(), OrgID: uuid.New(), NodeID: node, State: scan.StateDone}
	scans := &fakeScans{jobs: append(jobs, other), results: map[uuid.UUID][]byte{done: scanFixture(t)}}
	cs := connectOpts(t, f, tokenA(), func(o *Options) { o.Scans = scans })

	out := decode[DiskScansOut](t, call(t, cs, "disk_scans", map[string]any{"host": "web-1"}))
	if len(out.Jobs) != 5 || out.Jobs[0].State != "running" || out.Jobs[2].ID != done.String() || out.Jobs[2].Finished == "" || out.Jobs[2].ResultBytes != 1234 {
		t.Fatalf("jobs = %+v", out.Jobs)
	}
	r := out.Result
	if r == nil || r.JobID != done.String() || r.Root != "/" || r.FS == nil || r.FS.Free != 40 || r.ScannedBytes != 4000 {
		t.Fatalf("result = %+v", r)
	}
	if len(r.Top) != 20 || r.Top[0] != (ScanPath{"/var/f25", 250}) || r.Top[19].Path != "/var/f06" {
		t.Fatalf("top = %+v", r.Top)
	}
	for _, p := range r.Top {
		if p.Path == "/tmp" || strings.Contains(p.Path, "small") {
			t.Errorf("top has %q", p.Path)
		}
	}
	if len(r.Recs) != 1 || r.Recs[0].Label != "Old logs" || !r.Recs[0].Elevated {
		t.Errorf("recs = %+v", r.Recs)
	}

	t.Run("job id", func(t *testing.T) {
		out := decode[DiskScansOut](t, call(t, cs, "disk_scans", map[string]any{"host": "web-1", "job_id": done.String()}))
		if out.Result == nil || out.Result.JobID != done.String() {
			t.Fatalf("result = %+v", out.Result)
		}
		res := call(t, cs, "disk_scans", map[string]any{"host": "web-1", "job_id": jobs[0].ID.String()})
		if !res.IsError || !strings.Contains(text(res), "not done") {
			t.Fatalf("running job: text = %q", text(res))
		}
	})
	t.Run("other org", func(t *testing.T) {
		res := call(t, cs, "disk_scans", map[string]any{"host": "web-1", "job_id": other.ID.String()})
		if !res.IsError || text(res) != "disk scan not found" {
			t.Fatalf("IsError = %v, text = %q", res.IsError, text(res))
		}
		res = call(t, cs, "disk_scans", map[string]any{"host": "web-1", "job_id": uuid.NewString()})
		if !res.IsError || text(res) != "disk scan not found" {
			t.Fatalf("unknown job: text = %q", text(res))
		}
	})
	t.Run("no done job", func(t *testing.T) {
		out := decode[DiskScansOut](t, call(t, cs, "disk_scans", map[string]any{"host": "db-1"}))
		if len(out.Jobs) != 0 || out.Result != nil {
			t.Fatalf("out = %+v", out)
		}
	})
	t.Run("scans off", func(t *testing.T) {
		cs := connect(t, f, tokenA())
		res := call(t, cs, "disk_scans", map[string]any{"host": "web-1"})
		if !res.IsError || text(res) != "disk scans are off" {
			t.Fatalf("IsError = %v, text = %q", res.IsError, text(res))
		}
	})
	t.Run("host", func(t *testing.T) {
		if res := call(t, cs, "disk_scans", map[string]any{"host": ""}); !res.IsError || !strings.Contains(text(res), "host is required") {
			t.Fatalf("text = %q", text(res))
		}
	})
}
