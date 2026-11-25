package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/store"
)

// stepSeries gives one point each minute for an hour that ends at now.
// The value is lo before the step and hi from the point at step.
func stepSeries(lo, hi float64, step int) []clickhouse.HistoryPoint {
	pts := make([]clickhouse.HistoryPoint, 60)
	for i := range pts {
		v := lo
		if i >= step {
			v = hi
		}
		pts[i] = clickhouse.HistoryPoint{TS: now.Add(time.Duration(i-60) * time.Minute), Value: v + float64(i%3)*0.1}
	}
	return pts
}

func changeRequest(metric string) ChangeRequest {
	return ChangeRequest{NodeID: hostA, Metric: metric, From: now.Add(-time.Hour)}
}

func TestExplainChange(t *testing.T) {
	cp := now.Add(-30 * time.Minute)
	proc := func(name string, cpu float64) clickhouse.TopProc {
		return clickhouse.TopProc{NodeID: hostA, Name: name, Cmd: name + " --serve", CmdHash: uint64(len(name)), CPUPctAvg: cpu, RSSMax: uint64(cpu) << 20}
	}
	r := &fakeReader{
		history: stepSeries(10, 50, 30),
		procsAt: func(q clickhouse.RangeQuery) []clickhouse.TopProc {
			if q.From.Before(cp) {
				return []clickhouse.TopProc{proc("api", 20), proc("cron", 15), proc("idle", 1)}
			}
			return []clickhouse.TopProc{proc("api", 70), proc("batch", 30), proc("idle", 1)}
		},
		incidents: []clickhouse.IncidentRow{
			incident(uuid.New(), hostA, "threshold_crossed", "cpu", "warn", 0.75, nil),
			incident(uuid.New(), hostA, "threshold_crossed", "mem", "warn", 3, nil),
		},
	}
	s := newService(r, []store.Node{{ID: hostA, DisplayName: "web-1"}}, 0)
	got, err := s.ExplainChange(context.Background(), changeRequest("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 1 || !got.Points[0].TS.Equal(cp) || got.Points[0].Delta < 35 {
		t.Fatalf("points = %+v", got.Points)
	}
	if !got.Before.From.Equal(cp.Add(-30*time.Minute)) || !got.Before.To.Equal(cp) || !got.After.From.Equal(cp) || !got.After.To.Equal(now) {
		t.Fatalf("windows = %+v %+v", got.Before, got.After)
	}
	if got.By != clickhouse.ProcByCPU || r.gotBy != clickhouse.ProcByCPU {
		t.Errorf("by = %q, read with %q", got.By, r.gotBy)
	}
	if len(got.Processes) != 4 {
		t.Fatalf("processes = %+v", got.Processes)
	}
	if p := got.Processes[0]; p.Name != "api" || p.Delta != 50 || p.New || p.Gone {
		t.Errorf("first = %+v, want api +50", p)
	}
	if p := got.Processes[1]; p.Name != "batch" || !p.New || p.After != 30 {
		t.Errorf("second = %+v, want new batch", p)
	}
	if p := got.Processes[3]; p.Name != "cron" || !p.Gone || p.Delta != -15 {
		t.Errorf("last = %+v, want gone cron", p)
	}
	if len(got.Alerts) != 1 || got.Alerts[0].Metric != "cpu" {
		t.Errorf("alerts = %+v, want the cpu alert in the windows", got.Alerts)
	}
}

func TestExplainChangeFall(t *testing.T) {
	cp := now.Add(-30 * time.Minute)
	r := &fakeReader{
		history: stepSeries(80, 20, 30),
		procsAt: func(q clickhouse.RangeQuery) []clickhouse.TopProc {
			if q.From.Before(cp) {
				return []clickhouse.TopProc{{Name: "api", CPUPctAvg: 90}, {Name: "db", CPUPctAvg: 10}}
			}
			return []clickhouse.TopProc{{Name: "api", CPUPctAvg: 20}, {Name: "db", CPUPctAvg: 30}}
		},
	}
	s := newService(r, []store.Node{{ID: hostA}}, 0)
	got, err := s.ExplainChange(context.Background(), changeRequest("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Points[0].Delta > -50 || got.Processes[0].Name != "api" || got.Processes[0].Delta != -70 {
		t.Fatalf("fall: points %+v processes %+v", got.Points, got.Processes)
	}
}

func TestExplainChangeOrder(t *testing.T) {
	for metric, want := range map[string]clickhouse.ProcOrder{
		"cpu": clickhouse.ProcByCPU, "load_1": clickhouse.ProcByCPU, "psi_cpu": clickhouse.ProcByCPU, "procs": clickhouse.ProcByCPU,
		"net_rx": clickhouse.ProcByCPU, "mem": clickhouse.ProcByMem, "psi_mem": clickhouse.ProcByMem, "psi_mem_full": clickhouse.ProcByMem,
		"mem_pressure": clickhouse.ProcByMem, "disk_read": clickhouse.ProcByIO, "disk_write": clickhouse.ProcByIO,
		"psi_io": clickhouse.ProcByIO, "psi_io_full": clickhouse.ProcByIO,
	} {
		if got := processOrder(metric); got != want {
			t.Errorf("processOrder(%q) = %q, want %q", metric, got, want)
		}
	}
	r := &fakeReader{history: stepSeries(0.2, 0.8, 30), procs: []clickhouse.TopProc{{Name: "java", RSSMax: 4 << 30}}}
	s := newService(r, []store.Node{{ID: hostA}}, 0)
	got, err := s.ExplainChange(context.Background(), changeRequest("mem"))
	if err != nil {
		t.Fatal(err)
	}
	if got.By != clickhouse.ProcByMem || r.gotBy != clickhouse.ProcByMem {
		t.Fatalf("by = %q, read with %q", got.By, r.gotBy)
	}
	if len(got.Processes) != 0 && got.Processes[0].Before != 4<<30 {
		t.Errorf("mem values are bytes: %+v", got.Processes)
	}
}

func TestExplainChangeNoChange(t *testing.T) {
	pts := stepSeries(10, 10, 30)
	for i := range pts {
		pts[i].Value = 10
	}
	r := &fakeReader{history: pts}
	s := newService(r, []store.Node{{ID: hostA}}, 0)
	got, err := s.ExplainChange(context.Background(), changeRequest("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Points == nil || len(got.Points) != 0 || got.Processes == nil || got.Alerts == nil {
		t.Fatalf("lists must be empty, not nil: %+v", got)
	}
	if r.gotN != 0 {
		t.Error("no change point must not read processes")
	}
}

func TestExplainChangeErrors(t *testing.T) {
	s := newService(&fakeReader{history: stepSeries(1, 2, 30)}, []store.Node{{ID: hostA}}, 0)
	ctx := context.Background()
	if _, err := s.ExplainChange(ctx, changeRequest("bogus")); !errors.Is(err, clickhouse.ErrUnknownMetric) {
		t.Errorf("metric: err = %v", err)
	}
	r := changeRequest("cpu")
	r.NodeID = uuid.New()
	if _, err := s.ExplainChange(ctx, r); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("node: err = %v", err)
	}
	off := newService(nil, []store.Node{{ID: hostA}}, 0)
	if _, err := off.ExplainChange(ctx, changeRequest("cpu")); !errors.Is(err, ErrNoHistory) {
		t.Errorf("no history: err = %v", err)
	}
}

func TestExplainChangeClamp(t *testing.T) {
	r := &fakeReader{history: stepSeries(10, 50, 30)}
	s := newService(r, []store.Node{{ID: hostA}}, 1)
	req := changeRequest("cpu")
	req.From, req.To = now.Add(-72*time.Hour), now.Add(-48*time.Hour)
	got, err := s.ExplainChange(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Clamped || len(got.Points) != 0 || !got.From.Equal(got.To) {
		t.Fatalf("a range older than the plan must give an empty clamped report: %+v", got)
	}
	req.From, req.To = now.Add(-72*time.Hour), time.Time{}
	if got, err = s.ExplainChange(context.Background(), req); err != nil || !got.Clamped || !got.From.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("clamp: %+v, %v", got, err)
	}
}
