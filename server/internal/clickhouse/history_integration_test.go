//go:build integration

package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

// Two hours of samples each 10 s. CPU goes 20, 60, 20, 60, so each
// bucket has an average of 40 and a max of 60.
func TestHistoryTiers(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-2 * time.Hour)
	var bundles []IngestBundle
	for i, ts := 0, start; ts.Before(end); i, ts = i+1, ts.Add(10*time.Second) {
		cpu := float32(20)
		if i%2 == 1 {
			cpu = 60
		}
		bundles = append(bundles, IngestBundle{
			Sample: SampleRow{TS: ts, OrgID: org, NodeID: node, SchemaV: 1, CPUPct: cpu, MemUsed: 1, MemTotal: 4, Errors: []string{}},
			Disks: []DiskRow{
				{TS: ts, OrgID: org, NodeID: node, Mount: "/", FS: "ext4", Used: uint64(i), Total: 1000},
				{TS: ts, OrgID: org, NodeID: node, Mount: "/data", FS: "xfs", Used: 5, Total: 10},
			},
		})
	}
	if err := client.insert(ctx, bundles); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, tc := range []struct {
		name   string
		from   time.Time
		step   time.Duration
		res    time.Duration
		points int
	}{
		{"raw", end.Add(-time.Hour), 0, 0, 360},
		{"raw by step", end.Add(-time.Hour), 15 * time.Minute, 15 * time.Minute, 4},
		{"1m", end.Add(-2 * 24 * time.Hour), time.Minute, time.Minute, 120},
		{"5m", end.Add(-10 * 24 * time.Hour), 0, 5 * time.Minute, 24},
		{"1h", end.Add(-60 * 24 * time.Hour), 0, time.Hour, 2},
		{"1h by day", end.Add(-60 * 24 * time.Hour), 24 * time.Hour, 24 * time.Hour, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pts, res, err := client.QueryHistory(ctx, HistoryQuery{
				OrgID: org, NodeID: node, Metric: "cpu", From: tc.from, To: end.Add(-time.Millisecond), Step: tc.step,
			})
			if err != nil {
				t.Fatal(err)
			}
			if res != tc.res {
				t.Errorf("resolution = %v, want %v", res, tc.res)
			}
			if tc.points > 0 && len(pts) != tc.points {
				t.Errorf("points = %d, want %d", len(pts), tc.points)
			}
			if len(pts) == 0 {
				t.Fatal("no points")
			}
			if tc.res == 0 {
				return
			}
			for _, p := range pts {
				if math.Abs(p.Value-40) > 0.01 || p.Max != 60 {
					t.Fatalf("point %v: value %v max %v, want 40 and 60", p.TS, p.Value, p.Max)
				}
			}
		})
	}

	mem, _, err := client.QueryHistory(ctx, HistoryQuery{OrgID: org, NodeID: node, Metric: "mem", From: start, To: end, Step: time.Hour})
	if err != nil || len(mem) == 0 || math.Abs(mem[0].Value-0.25) > 0.001 {
		t.Fatalf("mem = %v, err %v, want 0.25", mem, err)
	}

	disks, bucket, err := client.DiskHistory(ctx, RangeQuery{OrgID: org, NodeID: node, From: start, To: end})
	if err != nil {
		t.Fatal(err)
	}
	if bucket != time.Minute {
		t.Errorf("disk bucket = %v, want 1m", bucket)
	}
	mounts := map[string]int{}
	for _, d := range disks {
		mounts[d.Mount]++
	}
	if mounts["/"] != 120 || mounts["/data"] != 120 {
		t.Fatalf("disk points = %v, want 120 for each mount", mounts)
	}
	// The last value of the bucket wins.
	if last := disks[119]; last.Mount != "/" || last.Used != uint64(len(bundles)-1) || last.FS != "ext4" {
		t.Fatalf("last / point = %+v", last)
	}
}

// 30 processes each 10 s for 3 minutes. Process pN has N % CPU.
func TestProcsTop1m(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	var engine string
	if err := client.conn.QueryRow(ctx, "SELECT engine_full FROM system.tables WHERE database = ? AND name = 'procs_topn'", client.db).Scan(&engine); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(engine, "toIntervalHour(1)") && !strings.Contains(engine, "INTERVAL 1 HOUR") {
		t.Fatalf("procs_topn TTL: %s", engine)
	}

	org, node := uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	var procs []ProcRow
	for ts := start; ts.Before(start.Add(3 * time.Minute)); ts = ts.Add(10 * time.Second) {
		for n := 1; n <= 30; n++ {
			procs = append(procs, ProcRow{
				TS: ts, OrgID: org, NodeID: node, PID: int32(n), Name: fmt.Sprintf("p%d", n),
				Cmd: fmt.Sprintf("/bin/p%d --flag", n), CPUPct: float32(n), RSS: uint64(n) << 20, StartTS: start,
			})
		}
	}
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: start, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	mv := client.table("procs_top_1m_mv")
	if err := client.conn.Exec(ctx, "SYSTEM REFRESH VIEW "+mv); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := client.conn.Exec(ctx, "SYSTEM WAIT VIEW "+mv); err != nil {
		t.Fatalf("wait: %v", err)
	}

	rows, err := client.conn.Query(ctx,
		"SELECT ts_minute, count(), argMin(name, rank), min(rank), max(rank) FROM "+client.table("procs_top_1m")+" FINAL "+
			"WHERE org_id = ? AND node_id = ? GROUP BY ts_minute ORDER BY ts_minute", org, node)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	minutes := 0
	for rows.Next() {
		var (
			ts     time.Time
			n      uint64
			top    string
			lo, hi uint8
		)
		if err := rows.Scan(&ts, &n, &top, &lo, &hi); err != nil {
			t.Fatal(err)
		}
		minutes++
		if n != 10 || top != "p30" || lo != 1 || hi != 10 {
			t.Errorf("minute %v: %d rows, top %s, ranks %d..%d; want 10 rows, top p30, ranks 1..10", ts, n, top, lo, hi)
		}
	}
	if minutes == 0 {
		t.Fatal("procs_top_1m is empty")
	}

	top, err := client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: start, To: start.Add(3 * time.Minute)}, ProcByCPU, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top[0].Name != "p30" || top[0].CPUPctAvg != 30 || top[0].RSSMax != 30<<20 || top[0].Cmd != "/bin/p30 --flag" {
		t.Fatalf("top from procs_topn = %+v", top)
	}
	top, err = client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: start.Add(-24 * time.Hour), To: start.Add(3 * time.Minute)}, ProcByCPU, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top[0].Name != "p30" || top[0].CPUPctMax != 30 {
		t.Fatalf("top from procs_top_1m = %+v", top)
	}
}

// procs_top_1h ranks the minutes of the hour.
func TestProcsTop1h(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	var rows []string
	for m := 0; m < 60; m++ {
		for n := 1; n <= 10; n++ {
			rows = append(rows, fmt.Sprintf("('%s', '%s', '%s', %d, 'p%d', %d, '/bin/p%d', %d, %d, %d, now())",
				hour.Add(time.Duration(m)*time.Minute).Format("2006-01-02 15:04:05"), org, node, 11-n, n, n, n, n, n, n))
		}
	}
	if err := client.conn.Exec(ctx, "INSERT INTO "+client.table("procs_top_1m")+
		" (ts_minute, org_id, node_id, rank, name, cmd_hash, cmd, cpu_pct_avg, cpu_pct_max, rss_max, refreshed_at) VALUES "+strings.Join(rows, ",")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	mv := client.table("procs_top_1h_mv")
	if err := client.conn.Exec(ctx, "SYSTEM REFRESH VIEW "+mv); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := client.conn.Exec(ctx, "SYSTEM WAIT VIEW "+mv); err != nil {
		t.Fatalf("wait: %v", err)
	}
	top, err := client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: hour.Add(-30 * 24 * time.Hour), To: hour.Add(time.Hour)}, ProcByCPU, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 5 || top[0].Name != "p10" || math.Abs(top[0].CPUPctAvg-10) > 0.01 || top[4].Name != "p6" {
		t.Fatalf("top from procs_top_1h = %+v", top)
	}
}

// A short window that is old reads the rollup, not procs_topn.
func TestTopProcessesOldShortWindow(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	minute := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Hour)
	var rows []string
	for n := 1; n <= 3; n++ {
		rows = append(rows, fmt.Sprintf("('%s', '%s', '%s', %d, 'p%d', %d, '/bin/p%d', %d, %d, %d, now())",
			minute.Format("2006-01-02 15:04:05"), org, node, 4-n, n, n, n, n, n, n))
	}
	if err := client.conn.Exec(ctx, "INSERT INTO "+client.table("procs_top_1m")+
		" (ts_minute, org_id, node_id, rank, name, cmd_hash, cmd, cpu_pct_avg, cpu_pct_max, rss_max, refreshed_at) VALUES "+strings.Join(rows, ",")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	top, err := client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: minute, To: minute.Add(time.Minute)}, ProcByCPU, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top[0].Name != "p3" {
		t.Fatalf("top = %+v, want 3 rows from procs_top_1m", top)
	}
}

// The multi query gives the same points as the single query, for each
// metric and each tier.
func TestQueryHistoryMultiMatchesSingle(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node, other := uuid.New(), uuid.New(), uuid.New()
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-2 * time.Hour)
	var bundles []IngestBundle
	for i, ts := 0, start; ts.Before(end); i, ts = i+1, ts.Add(10*time.Second) {
		for _, n := range []uuid.UUID{node, other} {
			bundles = append(bundles, IngestBundle{Sample: SampleRow{
				TS: ts, OrgID: org, NodeID: n, SchemaV: 1, CPUPct: float32(i % 100), MemUsed: uint64(1 + i%3), MemTotal: 4,
				NetRxBps: uint64(i), NetTxBps: uint64(2 * i), DiskReadBps: uint64(3 * i), DiskWriteBps: uint64(4 * i),
				ProcsTotal: uint32(100 + i%7), CPULoad1: float32(i%5) / 2, Errors: []string{},
			}})
		}
	}
	if err := client.insert(ctx, bundles); err != nil {
		t.Fatalf("insert: %v", err)
	}

	all := []string{"cpu", "mem", "net_rx", "net_tx", "disk_read", "disk_write", "procs", "load_1"}
	for _, tc := range []struct {
		name    string
		from    time.Time
		step    time.Duration
		metrics []string
	}{
		{"raw", end.Add(-time.Hour), 0, all},
		{"raw by step", end.Add(-time.Hour), 5 * time.Minute, all},
		{"1m", end.Add(-2 * 24 * time.Hour), time.Minute, all},
		{"5m", end.Add(-10 * 24 * time.Hour), 0, all},
	} {
		t.Run(tc.name, func(t *testing.T) {
			to := end.Add(-time.Millisecond)
			multi, res, err := client.QueryHistoryMulti(ctx, HistoryMultiQuery{
				OrgID: org, NodeIDs: []uuid.UUID{node, other}, Metrics: tc.metrics, From: tc.from, To: to, Step: tc.step,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range []uuid.UUID{node, other} {
				for _, m := range tc.metrics {
					single, sres, err := client.QueryHistory(ctx, HistoryQuery{OrgID: org, NodeID: n, Metric: m, From: tc.from, To: to, Step: tc.step})
					if err != nil {
						t.Fatal(err)
					}
					if sres != res {
						t.Errorf("%s: resolution %v, want %v", m, res, sres)
					}
					got := multi[n][m]
					if len(got) == 0 || len(got) != len(single) {
						t.Fatalf("%s: %d points, want %d", m, len(got), len(single))
					}
					for i := range got {
						if !got[i].TS.Equal(single[i].TS) || math.Abs(got[i].Value-single[i].Value) > 1e-9 || math.Abs(got[i].Max-single[i].Max) > 1e-9 {
							t.Fatalf("%s point %d: %+v, want %+v", m, i, got[i], single[i])
						}
					}
				}
			}
		})
	}

	_, _, err := client.QueryHistoryMulti(ctx, HistoryMultiQuery{
		OrgID: org, NodeIDs: []uuid.UUID{node}, Metrics: []string{"procs"}, From: end.Add(-2 * 24 * time.Hour), To: end, Step: time.Minute,
	})
	if err != nil {
		t.Fatalf("procs on 1m tier: %v", err)
	}
	_, _, err = client.QueryHistoryMulti(ctx, HistoryMultiQuery{
		OrgID: org, NodeIDs: []uuid.UUID{node}, Metrics: []string{"cpu", "nope"}, From: end.Add(-2 * 24 * time.Hour), To: end, Step: time.Minute,
	})
	if !errors.Is(err, ErrUnknownMetric) {
		t.Fatalf("unknown metric: err = %v, want ErrUnknownMetric", err)
	}
}

// A node of another org gives no series.
func TestQueryHistoryMultiOrgFilter(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, foreign := uuid.New(), uuid.New()
	mine, theirs := uuid.New(), uuid.New()
	end := time.Now().UTC().Truncate(time.Minute)
	var bundles []IngestBundle
	for i := 0; i < 6; i++ {
		ts := end.Add(-time.Duration(i+1) * 10 * time.Second)
		bundles = append(bundles,
			IngestBundle{Sample: SampleRow{TS: ts, OrgID: org, NodeID: mine, SchemaV: 1, CPUPct: 10, MemTotal: 1, Errors: []string{}}},
			IngestBundle{Sample: SampleRow{TS: ts, OrgID: foreign, NodeID: theirs, SchemaV: 1, CPUPct: 90, MemTotal: 1, Errors: []string{}}},
		)
	}
	if err := client.insert(ctx, bundles); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _, err := client.QueryHistoryMulti(ctx, HistoryMultiQuery{
		OrgID: org, NodeIDs: []uuid.UUID{mine, theirs}, Metrics: []string{"cpu"}, From: end.Add(-time.Hour), To: end, Step: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[theirs]; ok {
		t.Fatal("series of a foreign node")
	}
	if len(got[mine]["cpu"]) == 0 {
		t.Fatal("no series for the own node")
	}
}

// The newest sample of each node wins. A node with only old samples, and
// a node of another org, have no row.
func TestLatestByNode(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, n1, n2, foreign := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	row := func(org, node uuid.UUID, age time.Duration, cpu float32, used uint64) IngestBundle {
		return IngestBundle{Sample: SampleRow{
			TS: now.Add(-age), OrgID: org, NodeID: node, SchemaV: 1, CPUPct: cpu, CPULoad1: 2.5,
			MemUsed: used, MemTotal: 4, NetRxBps: 100, NetTxBps: 200, Errors: []string{},
		}}
	}
	if err := client.insert(ctx, []IngestBundle{
		row(org, n1, time.Minute, 10, 1),
		row(org, n1, 5*time.Second, 90, 3),
		row(org, n2, 30*time.Minute, 50, 2),
		row(foreign, foreign, time.Second, 70, 1),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := client.LatestByNode(ctx, org, now.Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %+v, want only node 1", got)
	}
	l := got[0]
	if l.NodeID != n1 || l.CPUPct != 90 || math.Abs(l.MemRatio-0.75) > 1e-9 || l.Load1 != 2.5 || l.NetRxBps != 100 || l.NetTxBps != 200 {
		t.Fatalf("latest = %+v", l)
	}
	if d := l.TS.Sub(now.Add(-5 * time.Second)); d < -time.Second || d > time.Second {
		t.Fatalf("ts = %v, want about 5 s ago", l.TS)
	}
	all, err := client.LatestByNode(ctx, org, now.Add(-time.Hour))
	if err != nil || len(all) != 2 {
		t.Fatalf("1 hour window: %+v, %v, want 2 nodes", all, err)
	}
}

// The ratio is the highest of the newest row of each mount.
func TestLatestDiskRatio(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, n1, n2 := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	d := func(node uuid.UUID, age time.Duration, mount string, used, total uint64) DiskRow {
		return DiskRow{TS: now.Add(-age), OrgID: org, NodeID: node, Mount: mount, FS: "ext4", Used: used, Total: total}
	}
	sample := func(node uuid.UUID, disks ...DiskRow) IngestBundle {
		return IngestBundle{Sample: SampleRow{TS: now, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Disks: disks}
	}
	if err := client.insert(ctx, []IngestBundle{
		sample(n1,
			d(n1, time.Minute, "/", 10, 10), // old row of a mount
			d(n1, 5*time.Second, "/", 9, 10),
			d(n1, 5*time.Second, "/data", 1, 10)),
		sample(n2, d(n2, 5*time.Second, "/", 50, 100)),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := client.LatestDiskRatio(ctx, org, now.Add(-10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || math.Abs(got[n1]-0.9) > 1e-9 || math.Abs(got[n2]-0.5) > 1e-9 {
		t.Fatalf("ratios = %v, want n1 0.9 and n2 0.5", got)
	}
}

// All nodes of the org, by CPU and by memory, in the raw and the rollup
// table.
func TestTopProcessesAllNodes(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, foreign, n1, n2 := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	type proc struct {
		node uuid.UUID
		name string
		cpu  float32
		rss  uint64
	}
	var bundles []IngestBundle
	for _, p := range []proc{
		{n1, "busy", 50, 10 << 20},
		{n1, "fat", 5, 500 << 20},
		{n2, "mid", 30, 100 << 20},
		{foreign, "other", 99, 900 << 20},
	} {
		o := org
		if p.node == foreign {
			o = foreign
		}
		var procs []ProcRow
		for ts := start; ts.Before(start.Add(3 * time.Minute)); ts = ts.Add(10 * time.Second) {
			procs = append(procs, ProcRow{TS: ts, OrgID: o, NodeID: p.node, PID: 1, Name: p.name, Cmd: "/bin/" + p.name, CPUPct: p.cpu, RSS: p.rss, StartTS: start})
		}
		bundles = append(bundles, IngestBundle{Sample: SampleRow{TS: start, OrgID: o, NodeID: p.node, SchemaV: 1, Errors: []string{}}, Procs: procs})
	}
	if err := client.insert(ctx, bundles); err != nil {
		t.Fatalf("insert: %v", err)
	}
	mv := client.table("procs_top_1m_mv")
	if err := client.conn.Exec(ctx, "SYSTEM REFRESH VIEW "+mv); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := client.conn.Exec(ctx, "SYSTEM WAIT VIEW "+mv); err != nil {
		t.Fatalf("wait: %v", err)
	}

	names := func(top []TopProc) (out []string) {
		for _, p := range top {
			out = append(out, p.Name)
		}
		return out
	}
	for _, tc := range []struct {
		name string
		from time.Time
	}{{"procs_topn", start}, {"procs_top_1m", start.Add(-24 * time.Hour)}} {
		t.Run(tc.name, func(t *testing.T) {
			q := RangeQuery{OrgID: org, From: tc.from, To: start.Add(3 * time.Minute)}
			byCPU, err := client.TopProcesses(ctx, q, ProcByCPU, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := names(byCPU); len(got) != 3 || got[0] != "busy" || got[1] != "mid" || got[2] != "fat" {
				t.Fatalf("by cpu = %v, want busy mid fat", got)
			}
			if byCPU[0].NodeID != n1 || byCPU[1].NodeID != n2 || byCPU[0].CPUPctAvg != 50 {
				t.Fatalf("by cpu = %+v", byCPU)
			}
			byMem, err := client.TopProcesses(ctx, q, ProcByMem, 2)
			if err != nil {
				t.Fatal(err)
			}
			if got := names(byMem); len(got) != 2 || got[0] != "fat" || got[1] != "mid" || byMem[0].RSSMax != 500<<20 {
				t.Fatalf("by mem = %+v, want fat then mid", byMem)
			}
			q.NodeID = n2
			one, err := client.TopProcesses(ctx, q, ProcByMem, 10)
			if err != nil || len(one) != 1 || one[0].Name != "mid" || one[0].NodeID != n2 {
				t.Fatalf("one node = %+v, %v", one, err)
			}
		})
	}
}

// The same as TestProcsTop1m with the memory order.
func TestTopProcessesByMem(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	var procs []ProcRow
	for ts := start; ts.Before(start.Add(time.Minute)); ts = ts.Add(10 * time.Second) {
		for n := 1; n <= 3; n++ {
			// p3 has the most CPU and the least memory.
			procs = append(procs, ProcRow{
				TS: ts, OrgID: org, NodeID: node, PID: int32(n), Name: fmt.Sprintf("p%d", n),
				Cmd: "c", CPUPct: float32(n), RSS: uint64(4-n) << 20, StartTS: start,
			})
		}
	}
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: start, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	q := RangeQuery{OrgID: org, NodeID: node, From: start, To: start.Add(time.Minute)}
	mem, err := client.TopProcesses(ctx, q, ProcByMem, 3)
	if err != nil || len(mem) != 3 || mem[0].Name != "p1" || mem[2].Name != "p3" {
		t.Fatalf("by mem = %+v, %v, want p1 first", mem, err)
	}
	cpu, err := client.TopProcesses(ctx, q, ProcByCPU, 3)
	if err != nil || len(cpu) != 3 || cpu[0].Name != "p3" {
		t.Fatalf("by cpu = %+v, %v, want p3 first", cpu, err)
	}
}

// Two hours of samples each 10 s. Node 1 has CPU 20 and 60 in turn, node 2
// has 10. Memory is 1 of 4.
func TestWindowStats(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, foreign, n1, n2, other := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-2 * time.Hour)
	var bundles []IngestBundle
	for i, ts := 0, start; ts.Before(end); i, ts = i+1, ts.Add(10*time.Second) {
		cpu := float32(20)
		if i%2 == 1 {
			cpu = 60
		}
		for _, r := range []struct {
			org, node uuid.UUID
			cpu       float32
		}{{org, n1, cpu}, {org, n2, 10}, {foreign, other, 99}} {
			bundles = append(bundles, IngestBundle{Sample: SampleRow{
				TS: ts, OrgID: r.org, NodeID: r.node, SchemaV: 1, CPUPct: r.cpu, MemUsed: 1, MemTotal: 4, Errors: []string{},
			}})
		}
	}
	if err := client.insert(ctx, bundles); err != nil {
		t.Fatalf("insert: %v", err)
	}
	to := end.Add(-time.Millisecond)
	for _, tc := range []struct {
		name   string
		from   time.Time
		points int
	}{
		{"raw", end.Add(-time.Hour), 360},
		{"1m", end.Add(-2 * 24 * time.Hour), 120},
		{"5m", end.Add(-10 * 24 * time.Hour), 24},
		{"1h", end.Add(-60 * 24 * time.Hour), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := client.WindowStats(ctx, StatsQuery{OrgID: org, Metric: "cpu", From: tc.from, To: to})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 {
				t.Fatalf("stats = %+v, want 2 nodes of the org", got)
			}
			by := map[uuid.UUID]NodeStat{got[0].NodeID: got[0], got[1].NodeID: got[1]}
			a, b := by[n1], by[n2]
			if math.Abs(a.Avg-40) > 0.01 || a.Max != 60 || a.Points != tc.points {
				t.Errorf("node 1 = %+v, want avg 40, max 60, %d points", a, tc.points)
			}
			if math.Abs(b.Avg-10) > 0.01 || b.Max != 10 || b.Points != tc.points {
				t.Errorf("node 2 = %+v, want avg 10, max 10, %d points", b, tc.points)
			}
			one, err := client.WindowStats(ctx, StatsQuery{OrgID: org, NodeID: n2, Metric: "cpu", From: tc.from, To: to})
			if err != nil || len(one) != 1 || one[0].NodeID != n2 {
				t.Fatalf("one node = %+v, %v", one, err)
			}
		})
	}
	mem, err := client.WindowStats(ctx, StatsQuery{OrgID: org, NodeID: n1, Metric: "mem", From: end.Add(-2 * 24 * time.Hour), To: to})
	if err != nil || len(mem) != 1 || math.Abs(mem[0].Avg-0.25) > 1e-9 || math.Abs(mem[0].Max-0.25) > 1e-9 {
		t.Fatalf("mem on 1m = %+v, %v, want avg and max 0.25", mem, err)
	}
	if _, err := client.WindowStats(ctx, StatsQuery{OrgID: org, Metric: "bogus", From: start, To: end}); !errors.Is(err, ErrUnknownMetric) {
		t.Fatalf("unknown metric: %v", err)
	}
}

// procs_topn keeps the folders and the I/O rates. A row without them
// reads back empty, and a rate of 0 stays different from no rate.
func TestProcsTopnFolderColumns(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	ts := time.Now().UTC().Truncate(time.Second)
	rate, zero := uint64(4096), uint64(0)
	procs := []ProcRow{
		{TS: ts, OrgID: org, NodeID: node, PID: 1, Name: "node", Cwd: "/srv/app/api", Repo: "/srv/app", Parent: "zsh", ReadBps: &rate, WriteBps: &zero, StartTS: ts},
		{TS: ts, OrgID: org, NodeID: node, PID: 2, Name: "sh", StartTS: ts},
	}
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: ts, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, err := client.conn.Query(ctx,
		"SELECT pid, cwd, repo, parent, read_bps, write_bps FROM "+client.table("procs_topn")+" WHERE org_id = ? AND node_id = ? ORDER BY pid", org, node)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []ProcRow
	for rows.Next() {
		var p ProcRow
		if err := rows.Scan(&p.PID, &p.Cwd, &p.Repo, &p.Parent, &p.ReadBps, &p.WriteBps); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d", len(got))
	}
	a, b := got[0], got[1]
	if a.Cwd != "/srv/app/api" || a.Repo != "/srv/app" || a.Parent != "zsh" || a.ReadBps == nil || *a.ReadBps != 4096 || a.WriteBps == nil || *a.WriteBps != 0 {
		t.Errorf("row with folders = %+v", a)
	}
	if b.Cwd != "" || b.Repo != "" || b.Parent != "" || b.ReadBps != nil || b.WriteBps != nil {
		t.Errorf("row without folders = %+v", b)
	}
}

// The same name and cmd in two repos are two processes.
func TestTopProcessesSplitsByRepo(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	var procs []ProcRow
	for ts := start; ts.Before(start.Add(3 * time.Minute)); ts = ts.Add(10 * time.Second) {
		for _, r := range []struct {
			pid  int32
			repo string
			cpu  float32
		}{{1, "/w/a", 80}, {2, "/w/b", 20}, {3, "", 5}} {
			procs = append(procs, ProcRow{
				TS: ts, OrgID: org, NodeID: node, PID: r.pid, Name: "node", Cmd: "node vitest",
				CPUPct: r.cpu, RSS: 1 << 20, StartTS: start, Repo: r.repo, Cwd: r.repo + "/pkg", Parent: "zsh",
			})
		}
	}
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: start, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	top, err := client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: start, To: start.Add(3 * time.Minute)}, ProcByCPU, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top[0].Repo != "/w/a" || top[1].Repo != "/w/b" || top[2].Repo != "" {
		t.Fatalf("top = %+v", top)
	}
	if top[0].Cwd != "/w/a/pkg" || top[0].Parent != "zsh" || top[0].CPUPctAvg != 80 {
		t.Errorf("top[0] = %+v", top[0])
	}
}

// The minute rollup and the hour rollup keep the repo. The rollups have
// no cwd and no parent.
func TestTopProcessesRollupRepo(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	var procs []ProcRow
	for ts := start; ts.Before(start.Add(3 * time.Minute)); ts = ts.Add(10 * time.Second) {
		for _, r := range []struct {
			pid  int32
			repo string
			cpu  float32
		}{{1, "/w/a", 80}, {2, "/w/b", 20}} {
			procs = append(procs, ProcRow{
				TS: ts, OrgID: org, NodeID: node, PID: r.pid, Name: "node", Cmd: "node vitest",
				CPUPct: r.cpu, RSS: 1 << 20, StartTS: start, Repo: r.repo, Cwd: r.repo + "/pkg", Parent: "zsh",
			})
		}
	}
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: start, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	refresh := func(view string) {
		t.Helper()
		mv := client.table(view)
		if err := client.conn.Exec(ctx, "SYSTEM REFRESH VIEW "+mv); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		if err := client.conn.Exec(ctx, "SYSTEM WAIT VIEW "+mv); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}
	check := func(label string, q RangeQuery) {
		t.Helper()
		top, err := client.TopProcesses(ctx, q, ProcByCPU, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(top) != 2 || top[0].Repo != "/w/a" || top[1].Repo != "/w/b" || top[0].Cwd != "" || top[0].Parent != "" {
			t.Fatalf("%s: top = %+v", label, top)
		}
	}
	refresh("procs_top_1m_mv")
	check("procs_top_1m", RangeQuery{OrgID: org, NodeID: node, From: start.Add(-24 * time.Hour), To: start.Add(3 * time.Minute)})

	// procs_top_1h reads the rows of procs_top_1m of the hour before.
	hour := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	var rows []string
	for m := 0; m < 60; m++ {
		for n, repo := range []string{"/w/a", "/w/b"} {
			rows = append(rows, fmt.Sprintf("('%s', '%s', '%s', %d, 'node', 1, 'node vitest', %d, %d, 1, now(), '%s')",
				hour.Add(time.Duration(m)*time.Minute).Format("2006-01-02 15:04:05"), org, node, n+1, 80-60*n, 80-60*n, repo))
		}
	}
	if err := client.conn.Exec(ctx, "INSERT INTO "+client.table("procs_top_1m")+
		" (ts_minute, org_id, node_id, rank, name, cmd_hash, cmd, cpu_pct_avg, cpu_pct_max, rss_max, refreshed_at, repo) VALUES "+strings.Join(rows, ",")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	refresh("procs_top_1h_mv")
	check("procs_top_1h", RangeQuery{OrgID: org, NodeID: node, From: hour.Add(-30 * 24 * time.Hour), To: hour.Add(30 * time.Minute)})
}

func TestProcessKeyMatchesClickHouse(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	for _, cmd := range []string{"", "/usr/bin/node server.js", "/opt/café/日本語 --flag", strings.Repeat("x", 1024), strings.Repeat("y", 70)} {
		var got uint64
		if err := client.conn.QueryRow(context.Background(), "SELECT cityHash64(?)", cmd).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := wire.NewProcessKey("x", cmd).CmdHash; got != want {
			t.Errorf("cmd of %d bytes: ClickHouse %d, Go %d", len(cmd), got, want)
		}
	}
}

func TestProcsIOColumns(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	for _, table := range []string{"procs_top_1m", "procs_top_1h"} {
		var n uint64
		err := client.conn.QueryRow(context.Background(),
			"SELECT count() FROM system.columns WHERE database = ? AND table = ? AND name = 'io_bps_avg'", client.db, table).Scan(&n)
		if err != nil || n != 1 {
			t.Errorf("%s: io_bps_avg columns = %d, %v", table, n, err)
		}
	}
}

// insertTop inserts n rollup rows for one bucket. Process pN has N % CPU
// and N*100 B/s of I/O. The table is procs_top_1m or procs_top_1h.
func insertTop(t *testing.T, client *Client, table, tsCol string, org, node uuid.UUID, bucket time.Time, n int) {
	t.Helper()
	var rows []string
	for i := 1; i <= n; i++ {
		rows = append(rows, fmt.Sprintf("('%s', '%s', '%s', %d, 'p%d', %d, '/bin/p%d', %d, %d, %d, now(), %d)",
			bucket.Format("2006-01-02 15:04:05"), org, node, n+1-i, i, i, i, i, i*2, i<<20, i*100))
	}
	err := client.conn.Exec(context.Background(), "INSERT INTO "+client.table(table)+
		" ("+tsCol+", org_id, node_id, rank, name, cmd_hash, cmd, cpu_pct_avg, cpu_pct_max, rss_max, refreshed_at, io_bps_avg) VALUES "+strings.Join(rows, ","))
	if err != nil {
		t.Fatalf("insert %s: %v", table, err)
	}
}

func TestProcessesAtRaw(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	ts1 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second).Add(123 * time.Millisecond)
	ts2 := ts1.Add(10 * time.Second)
	var bundles []IngestBundle
	for i, ts := range []time.Time{ts1, ts2} {
		var procs []ProcRow
		for n := 1; n <= 4; n++ {
			bps := uint64(n * 10)
			procs = append(procs, ProcRow{
				TS: ts, OrgID: org, NodeID: node, PID: int32(100*(i+1) + n), PPID: 1, Name: fmt.Sprintf("p%d", n),
				Cmd: fmt.Sprintf("/bin/p%d", n), UserName: "root", Parent: "init", Cwd: "/srv", Repo: "/srv",
				CPUPct: float32(n * (i + 1)), RSS: uint64(n) << 20, Threads: 3, State: "S", StartTS: ts1.Add(-time.Hour),
				ReadBps: &bps, WriteBps: &bps,
			})
		}
		bundles = append(bundles, IngestBundle{
			Sample: SampleRow{TS: ts, OrgID: org, NodeID: node, SchemaV: 1, ProcsTotal: uint32(300 + i), Errors: []string{}},
			Procs:  procs,
		})
	}
	if err := client.insert(ctx, bundles); err != nil {
		t.Fatal(err)
	}

	snap, err := client.ProcessesAt(ctx, SnapshotQuery{OrgID: org, NodeID: node, At: ts1.Add(5 * time.Second), By: ProcByCPU, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tier != "raw" || !snap.TS.Equal(ts1) || snap.Bucket != 0 || snap.Total != 300 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Next == nil || !snap.Next.Equal(ts2) || snap.Prev != nil {
		t.Fatalf("prev %v next %v, want next %v", snap.Prev, snap.Next, ts2)
	}
	if len(snap.Procs) != 3 || snap.Procs[0].Name != "p4" || snap.Procs[0].PID != 104 || snap.Procs[0].CPUPct != 4 {
		t.Fatalf("procs = %+v", snap.Procs)
	}
	p := snap.Procs[0]
	if p.User != "root" || p.Parent != "init" || p.Cwd != "/srv" || p.IOBps != 80 || p.StartTS == nil ||
		p.CmdHash != wire.NewProcessKey("p4", "/bin/p4").CmdHash {
		t.Fatalf("proc = %+v", p)
	}

	snap, err = client.ProcessesAt(ctx, SnapshotQuery{OrgID: org, NodeID: node, At: ts2.Add(time.Second), By: ProcByIO, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !snap.TS.Equal(ts2) || snap.Total != 301 || snap.Next != nil || snap.Prev == nil || !snap.Prev.Equal(ts1) || len(snap.Procs) != 2 {
		t.Fatalf("second snapshot = %+v", snap)
	}
	// A time more than 2 minutes after the newest sample finds no sample.
	snap, err = client.ProcessesAt(ctx, SnapshotQuery{OrgID: org, NodeID: node, At: ts2.Add(3 * time.Minute), By: ProcByCPU, Limit: 3})
	if err != nil || len(snap.Procs) != 0 {
		t.Fatalf("late snapshot = %+v, %v", snap, err)
	}
}

func TestProcessesAtMinute(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	minute := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Hour)
	insertTop(t, client, "procs_top_1m", "ts_minute", org, node, minute, 12)
	snap, err := client.ProcessesAt(ctx, SnapshotQuery{OrgID: org, NodeID: node, At: minute.Add(30 * time.Second), By: ProcByCPU, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tier != "1m" || !snap.TS.Equal(minute) || snap.Bucket != time.Minute || len(snap.Procs) != 10 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Prev == nil || !snap.Prev.Equal(minute.Add(-time.Minute)) || snap.Next == nil || !snap.Next.Equal(minute.Add(time.Minute)) {
		t.Fatalf("prev %v next %v", snap.Prev, snap.Next)
	}
	if first := snap.Procs[0]; first.Name != "p12" || first.CPUPct != 12 || first.CPUPctMax != 24 || first.RSS != 12<<20 || first.IOBps != 1200 {
		t.Fatalf("first = %+v", first)
	}
	for _, p := range snap.Procs {
		k := wire.ProcessKey{Name: p.Name, CmdHash: p.CmdHash}
		if got, err := wire.ParseProcessKey(k.String()); err != nil || got != k {
			t.Errorf("key of %s: %v", p.Name, err)
		}
	}
	snap, err = client.ProcessesAt(ctx, SnapshotQuery{OrgID: org, NodeID: node, At: minute, By: ProcByIO, Limit: 2})
	if err != nil || len(snap.Procs) != 2 || snap.Procs[0].Name != "p12" {
		t.Fatalf("by io = %+v, %v", snap.Procs, err)
	}
}

func TestProcessesAtHour(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()

	org, node := uuid.New(), uuid.New()
	hour := time.Now().UTC().Truncate(time.Hour).Add(-10 * 24 * time.Hour)
	insertTop(t, client, "procs_top_1h", "ts_hour", org, node, hour, 7)
	snap, err := client.ProcessesAt(context.Background(), SnapshotQuery{OrgID: org, NodeID: node, At: hour.Add(25 * time.Minute), By: ProcByMem, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Tier != "1h" || !snap.TS.Equal(hour) || snap.Bucket != time.Hour || len(snap.Procs) != 5 || snap.Procs[0].Name != "p7" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestProcessesAtEmpty(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()

	now := time.Now().UTC()
	for _, at := range []time.Time{now.Add(-time.Minute), now.Add(-3 * time.Hour), now.Add(-10 * 24 * time.Hour)} {
		snap, err := client.ProcessesAt(context.Background(), SnapshotQuery{OrgID: uuid.New(), NodeID: uuid.New(), At: at, By: ProcByCPU, Limit: 20})
		if err != nil || len(snap.Procs) != 0 {
			t.Errorf("at %v: %+v, %v", at, snap, err)
		}
	}
}

// 3 processes. busy has the most CPU, disk has the most I/O.
func TestTopProcessesByIO(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	var procs []ProcRow
	for ts := start; ts.Before(start.Add(3 * time.Minute)); ts = ts.Add(10 * time.Second) {
		for i, p := range []struct {
			name   string
			cpu    float32
			rd, wr uint64
		}{{"busy", 90, 0, 0}, {"disk", 10, 4000, 6000}, {"idle", 1, 100, 0}} {
			rd, wr := p.rd, p.wr
			procs = append(procs, ProcRow{
				TS: ts, OrgID: org, NodeID: node, PID: int32(i + 1), Name: p.name, Cmd: "/bin/" + p.name,
				CPUPct: p.cpu, RSS: 1 << 20, StartTS: start, ReadBps: &rd, WriteBps: &wr,
			})
		}
	}
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: start, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs}}); err != nil {
		t.Fatal(err)
	}
	mv := client.table("procs_top_1m_mv")
	if err := client.conn.Exec(ctx, "SYSTEM REFRESH VIEW "+mv); err != nil {
		t.Fatal(err)
	}
	if err := client.conn.Exec(ctx, "SYSTEM WAIT VIEW "+mv); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		from time.Time
	}{{"procs_topn", start}, {"procs_top_1m", start.Add(-24 * time.Hour)}} {
		t.Run(tc.name, func(t *testing.T) {
			top, err := client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: tc.from, To: start.Add(3 * time.Minute)}, ProcByIO, 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(top) != 3 || top[0].Name != "disk" || top[0].IOBpsAvg != 10000 || top[1].Name != "idle" || top[2].Name != "busy" {
				t.Fatalf("by io = %+v", top)
			}
			if top[0].CmdHash != wire.NewProcessKey("disk", "/bin/disk").CmdHash {
				t.Fatalf("cmd hash = %d", top[0].CmdHash)
			}
		})
	}
}

// Two processes over 10 min of procs_topn, 1 min step.
func TestProcessSeriesRaw(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)
	var procs []ProcRow
	for ts := start; ts.Before(start.Add(10 * time.Minute)); ts = ts.Add(10 * time.Second) {
		m := float32(ts.Sub(start) / time.Minute)
		for i, name := range []string{"a", "b"} {
			rd, wr := uint64(100*(i+1)), uint64(50)
			procs = append(procs, ProcRow{
				TS: ts, OrgID: org, NodeID: node, PID: int32(i + 1), Name: name, Cmd: "/bin/" + name,
				CPUPct: m * float32(i+1), RSS: uint64(m+1) << 20, StartTS: start, ReadBps: &rd, WriteBps: &wr,
			})
		}
	}
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: start, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs}}); err != nil {
		t.Fatal(err)
	}
	keyA, keyB := wire.NewProcessKey("a", "/bin/a"), wire.NewProcessKey("b", "/bin/b")
	got, err := client.ProcessSeries(ctx, ProcSeriesQuery{
		RangeQuery: RangeQuery{OrgID: org, NodeID: node, From: start, To: start.Add(10*time.Minute - time.Second)},
		Keys:       []wire.ProcessKey{keyA, keyB, wire.NewProcessKey("c", "/bin/c")},
		Step:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("keys = %d, want 2", len(got))
	}
	for key, mul := range map[wire.ProcessKey]float64{keyA: 1, keyB: 2} {
		pts := got[key]
		if len(pts) != 10 {
			t.Fatalf("%s: %d points, want 10", key.Name, len(pts))
		}
		for i, p := range pts {
			if !p.TS.Equal(start.Add(time.Duration(i)*time.Minute)) || p.CPUPct != float64(i)*mul ||
				p.RSS != uint64(i+1)<<20 || p.IOBps != 100*mul+50 {
				t.Errorf("%s[%d] = %+v", key.Name, i, p)
			}
		}
	}
}

// procs_top_1m rows for 2 h, 3 h ago: the points are on the minute buckets.
func TestProcessSeriesMinuteTier(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	for m := 0; m < 120; m++ {
		insertTop(t, client, "procs_top_1m", "ts_minute", org, node, start.Add(time.Duration(m)*time.Minute), 2)
	}
	key := wire.NewProcessKey("p2", "/bin/p2")
	// insertTop stores cmd_hash = i, so the key hash is 2.
	key.CmdHash = 2
	to := start.Add(2 * time.Hour)
	for _, step := range []time.Duration{time.Minute, 10 * time.Minute} {
		got, err := client.ProcessSeries(ctx, ProcSeriesQuery{RangeQuery: RangeQuery{OrgID: org, NodeID: node, From: start, To: to}, Keys: []wire.ProcessKey{key}, Step: step})
		if err != nil {
			t.Fatal(err)
		}
		pts := got[key]
		if want := int(2 * time.Hour / step); len(pts) != want && len(pts) != want+1 {
			t.Fatalf("step %v: %d points, want %d", step, len(pts), want)
		}
		for _, p := range pts {
			if p.TS.Unix()%int64(step/time.Second) != 0 || p.CPUPct != 2 || p.RSS != 2<<20 || p.IOBps != 200 {
				t.Fatalf("step %v: point %+v", step, p)
			}
		}
	}
}

func TestProcessFacts(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	start := now.Add(-30 * time.Minute)
	var procs []ProcRow
	for i, ts := range []time.Time{now.Add(-20 * time.Second), now.Add(-10 * time.Second), now.Add(10 * time.Second)} {
		procs = append(procs,
			ProcRow{TS: ts, OrgID: org, NodeID: node, PID: 100, PPID: int32(i + 1), Name: "nginx", Cmd: "/usr/sbin/nginx", UserName: "www",
				Cwd: "/srv", Repo: "/srv", Parent: "init", State: "S", Threads: 4, StartTS: start},
			ProcRow{TS: ts, OrgID: org, NodeID: node, PID: 200, Name: "other", Cmd: "/bin/other", StartTS: start})
	}
	ports := []PortRow{
		{TS: now.Add(-15 * time.Second), OrgID: org, NodeID: node, PID: 100, Name: "nginx", ListenTCP: []uint16{80}},
		{TS: now.Add(-5 * time.Second), OrgID: org, NodeID: node, PID: 100, Name: "nginx", ListenTCP: []uint16{80, 443}, ListenUDP: []uint16{53}},
		// Another process with the same PID before the start of nginx.
		{TS: start.Add(-time.Hour), OrgID: org, NodeID: node, PID: 100, Name: "old", ListenTCP: []uint16{1}},
		{TS: now.Add(-5 * time.Second), OrgID: org, NodeID: node, PID: 200, Name: "other", ListenTCP: []uint16{9}},
	}
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: now, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs, Ports: ports}}); err != nil {
		t.Fatal(err)
	}

	key := wire.NewProcessKey("nginx", "/usr/sbin/nginx")
	f, ok, err := client.ProcessFacts(ctx, org, node, key, now)
	if err != nil || !ok {
		t.Fatalf("facts: %v %v", ok, err)
	}
	if f.PID != 100 || f.PPID != 2 || f.User != "www" || f.Cwd != "/srv" || f.Parent != "init" || f.Threads != 4 || f.State != "S" ||
		!f.SeenAt.Equal(now.Add(-10*time.Second)) || f.StartTS == nil || !f.StartTS.Equal(start) {
		t.Errorf("facts = %+v", f)
	}
	if len(f.ListenTCP) != 2 || f.ListenTCP[1] != 443 || len(f.ListenUDP) != 1 || f.ListenUDP[0] != 53 {
		t.Errorf("ports = %v %v", f.ListenTCP, f.ListenUDP)
	}

	// An earlier time gives the earlier row and the earlier ports.
	f, ok, err = client.ProcessFacts(ctx, org, node, key, now.Add(-12*time.Second))
	if err != nil || !ok || f.PPID != 1 || len(f.ListenTCP) != 1 || f.ListenTCP[0] != 80 || len(f.ListenUDP) != 0 {
		t.Errorf("earlier facts = %+v %v %v", f, ok, err)
	}
	// More than 1 h later there is no row.
	if _, ok, err := client.ProcessFacts(ctx, org, node, key, now.Add(2*time.Hour)); err != nil || ok {
		t.Errorf("late facts: %v %v", ok, err)
	}
	// An unknown key and another node have no row.
	if _, ok, err := client.ProcessFacts(ctx, org, node, wire.NewProcessKey("nginx", "/other"), now); err != nil || ok {
		t.Errorf("unknown key: %v %v", ok, err)
	}
	if _, ok, err := client.ProcessFacts(ctx, org, uuid.New(), key, now); err != nil || ok {
		t.Errorf("other node: %v %v", ok, err)
	}

	cmd, repo, err := client.ProcessInfo(ctx, org, node, key, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil || cmd != "/usr/sbin/nginx" || repo != "/srv" {
		t.Errorf("info = %q %q %v", cmd, repo, err)
	}
}

func TestProcessInfoNoRow(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	now := time.Now().UTC()
	for _, from := range []time.Time{now.Add(-time.Minute), now.Add(-3 * time.Hour), now.Add(-10 * 24 * time.Hour)} {
		_, _, err := client.ProcessInfo(context.Background(), uuid.New(), uuid.New(), wire.NewProcessKey("x", "y"), from, from.Add(time.Minute))
		if !errors.Is(err, ErrNoProcess) {
			t.Errorf("from %v: err = %v, want ErrNoProcess", from, err)
		}
	}
}

// The info of a process in a rollup range comes from the rollup.
func TestProcessInfoRollup(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	org, node := uuid.New(), uuid.New()
	minute := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Hour)
	insertTop(t, client, "procs_top_1m", "ts_minute", org, node, minute, 2)
	key := wire.ProcessKey{Name: "p2", CmdHash: 2}
	cmd, _, err := client.ProcessInfo(context.Background(), org, node, key, minute, minute.Add(time.Minute))
	if err != nil || cmd != "/bin/p2" {
		t.Fatalf("info = %q %v", cmd, err)
	}
}

func f32(v float32) *float32 { return &v }
func u32(v uint32) *uint32   { return &v }
func u64(v uint64) *uint64   { return &v }
func u8(v uint8) *uint8      { return &v }

// Two hours of samples each 10 s, ending at the start of the hour. The
// linux node has zombies (0, 1, 2 in turn), PSI, and 50 of 100 PIDs. The
// mac node has only a memory pressure.
func insertHealthSamples(t *testing.T, client *Client, org, linux, mac uuid.UUID, end time.Time) {
	t.Helper()
	var bundles []IngestBundle
	for i, ts := 0, end.Add(-2*time.Hour); ts.Before(end); i, ts = i+1, ts.Add(10*time.Second) {
		bundles = append(bundles,
			IngestBundle{Sample: SampleRow{
				TS: ts, OrgID: org, NodeID: linux, SchemaV: 1, CPUPct: 20, CPULoad1: 1.5, ProcsTotal: 200, MemUsed: 1, MemTotal: 4, Errors: []string{},
				Zombies: u32(uint32(i % 3)), PidsUsed: u64(50), PidsMax: u64(100), FilesUsed: u64(10), FilesMax: u64(0),
				PSICPUSome: f32(5), PSIMemSome: f32(6), PSIMemFull: f32(7), PSIIOSome: f32(8), PSIIOFull: f32(9),
				DiskRatioMax: f32(0.75), InodesRatioMax: f32(0.25),
			}},
			IngestBundle{Sample: SampleRow{
				TS: ts, OrgID: org, NodeID: mac, SchemaV: 1, CPUPct: 10, CPULoad1: 0.5, ProcsTotal: 300, MemUsed: 1, MemTotal: 4, Errors: []string{},
				MemPressurePct: u8(40),
			}})
	}
	if err := client.insert(context.Background(), bundles); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-4 }

func TestHealthHistoryTiers(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, linux, mac := uuid.New(), uuid.New(), uuid.New()
	end := time.Now().UTC().Truncate(time.Hour)
	insertHealthSamples(t, client, org, linux, mac, end)

	metrics := []string{"zombies", "psi_cpu", "psi_mem", "psi_mem_full", "psi_io", "psi_io_full", "pids", "files", "disk", "inodes", "mem_pressure", "load", "procs"}
	for _, tc := range []struct {
		name string
		from time.Time
		step time.Duration
		raw  bool
	}{
		{"raw", end.Add(-time.Hour), 0, true},
		{"1m", end.Add(-2 * 24 * time.Hour), time.Minute, false},
		{"5m", end.Add(-10 * 24 * time.Hour), 0, false},
		{"1h", end.Add(-60 * 24 * time.Hour), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := client.QueryHistoryMulti(ctx, HistoryMultiQuery{
				OrgID: org, NodeIDs: []uuid.UUID{linux, mac}, Metrics: metrics, From: tc.from, To: end.Add(-time.Millisecond), Step: tc.step,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]float64{
				"psi_cpu": 5, "psi_mem": 6, "psi_mem_full": 7, "psi_io": 8, "psi_io_full": 9,
				"pids": 0.5, "disk": 0.75, "inodes": 0.25, "load": 1.5, "procs": 200,
			}
			for m, v := range want {
				pts := got[linux][m]
				if len(pts) == 0 {
					t.Errorf("linux %s: no points", m)
				}
				for _, p := range pts {
					if !near(p.Value, v) || !near(p.Max, v) {
						t.Errorf("linux %s: %+v, want %v", m, p, v)
						break
					}
				}
			}
			z := got[linux]["zombies"]
			if len(z) == 0 {
				t.Fatal("linux zombies: no points")
			}
			if tc.raw {
				if z[0].Value != 0 || z[1].Value != 1 || z[2].Value != 2 {
					t.Errorf("raw zombies = %+v", z[:3])
				}
			} else if !near(z[0].Value, 1) || !near(z[0].Max, 2) {
				t.Errorf("zombies = %+v, want avg 1, max 2", z[0])
			}
			// A metric with no value has no points: files has a zero max,
			// and the mac node has no PSI.
			if n := len(got[linux]["files"]); n != 0 {
				t.Errorf("linux files: %d points, want 0", n)
			}
			for _, m := range []string{"psi_cpu", "psi_io_full", "zombies", "pids", "disk", "inodes"} {
				if n := len(got[mac][m]); n != 0 {
					t.Errorf("mac %s: %d points, want 0", m, n)
				}
			}
			if pts := got[mac]["mem_pressure"]; len(pts) == 0 || !near(pts[0].Value, 40) {
				t.Errorf("mac mem_pressure = %+v", pts)
			}
			if pts := got[linux]["mem_pressure"]; len(pts) != 0 {
				t.Errorf("linux mem_pressure: %d points, want 0", len(pts))
			}
			if pts := got[mac]["procs"]; len(pts) == 0 || !near(pts[0].Value, 300) {
				t.Errorf("mac procs = %+v", pts)
			}
		})
	}
}

// The metrics of two families come from two tables on a rollup tier.
func TestQueryHistoryMultiFamilies(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, linux, mac := uuid.New(), uuid.New(), uuid.New()
	end := time.Now().UTC().Truncate(time.Hour)
	insertHealthSamples(t, client, org, linux, mac, end)

	got, res, err := client.QueryHistoryMulti(ctx, HistoryMultiQuery{
		OrgID: org, NodeIDs: []uuid.UUID{linux}, Metrics: []string{"cpu", "zombies", "load"},
		From: end.Add(-2 * 24 * time.Hour), To: end.Add(-time.Millisecond), Step: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res != time.Minute {
		t.Errorf("resolution = %v", res)
	}
	for m, v := range map[string]float64{"cpu": 20, "zombies": 1, "load": 1.5} {
		pts := got[linux][m]
		if len(pts) != 120 || !near(pts[0].Value, v) {
			t.Errorf("%s: %d points, first %+v, want 120 points of %v", m, len(pts), pts, v)
		}
	}
	// The single metric query reads the same table.
	one, _, err := client.QueryHistory(ctx, HistoryQuery{OrgID: org, NodeID: linux, Metric: "zombies", From: end.Add(-2 * 24 * time.Hour), To: end.Add(-time.Millisecond), Step: time.Minute})
	if err != nil || len(one) != 120 {
		t.Errorf("single zombies: %d points, %v", len(one), err)
	}
	// The mac node has no zombies: no points and no error.
	one, _, err = client.QueryHistory(ctx, HistoryQuery{OrgID: org, NodeID: mac, Metric: "zombies", From: end.Add(-2 * 24 * time.Hour), To: end.Add(-time.Millisecond), Step: time.Minute})
	if err != nil || len(one) != 0 {
		t.Errorf("mac zombies: %d points, %v", len(one), err)
	}
}

func TestDiskHistoryInodes(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	ts := time.Now().UTC().Truncate(time.Minute).Add(-10 * time.Minute)
	err := client.insert(ctx, []IngestBundle{
		{Sample: SampleRow{TS: ts, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Disks: []DiskRow{
			{TS: ts, OrgID: org, NodeID: node, Mount: "/", FS: "ext4", Used: 5, Total: 10, InodesUsed: u64(100), InodesTotal: u64(1000)},
			{TS: ts, OrgID: org, NodeID: node, Mount: "/data", FS: "btrfs", Used: 6, Total: 10},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A row from before the migration has null inodes.
	if err := client.conn.Exec(ctx, "INSERT INTO "+client.table("disks")+" (ts, org_id, node_id, mount, fs, used, total) VALUES (?, ?, ?, '/old', 'xfs', 1, 2)",
		ts, org, node); err != nil {
		t.Fatal(err)
	}
	pts, _, err := client.DiskHistory(ctx, RangeQuery{OrgID: org, NodeID: node, From: ts.Add(-time.Minute), To: ts.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DiskPoint{}
	for _, p := range pts {
		got[p.Mount] = p
	}
	if len(got) != 3 || got["/"].InodesUsed != 100 || got["/"].InodesTotal != 1000 || got["/data"].InodesTotal != 0 || got["/old"].InodesUsed != 0 || got["/old"].Used != 1 {
		t.Fatalf("points = %+v", pts)
	}
}

func TestHealthColumns(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()
	for table, cols := range map[string][]string{
		"samples_raw": {"zombies", "pids_used", "pids_max", "files_used", "files_max", "psi_cpu_some", "psi_mem_some", "psi_mem_full",
			"psi_io_some", "psi_io_full", "mem_pressure_pct", "disk_ratio_max", "inodes_ratio_max"},
		"disks":     {"inodes_used", "inodes_total"},
		"health_1m": {"ts_minute", "avg_load_1", "max_psi_io_full", "avg_pids"},
		"health_5m": {"ts_5m", "avg_procs", "max_mem_pressure"},
		"health_1h": {"ts_hour", "avg_inodes", "max_disk"},
	} {
		for _, col := range cols {
			var n uint64
			if err := client.conn.QueryRow(ctx, "SELECT count() FROM system.columns WHERE database = ? AND table = ? AND name = ?", client.db, table, col).Scan(&n); err != nil || n != 1 {
				t.Errorf("column %s.%s: count %d, err %v", table, col, n, err)
			}
		}
	}
}

// ProcessStarts gives the newest raw row of each key. FirstSeen gives the
// oldest 1 minute bucket.
func TestProcessStartsIntegration(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	start := time.Now().UTC().Truncate(time.Minute).Add(-5 * time.Minute)
	born := start.Add(-time.Hour)
	var procs []ProcRow
	for ts := start; ts.Before(start.Add(3 * time.Minute)); ts = ts.Add(10 * time.Second) {
		pid := int32(10)
		if ts.After(start.Add(time.Minute)) {
			pid = 11
		}
		procs = append(procs, ProcRow{TS: ts, OrgID: org, NodeID: node, PID: pid, Name: "api", Cmd: "/bin/api", CPUPct: 5, RSS: 1 << 20, StartTS: born})
	}
	procs = append(procs, ProcRow{TS: start, OrgID: org, NodeID: node, PID: 3, Name: "nostart", Cmd: "/bin/nostart", CPUPct: 1, RSS: 1 << 20})
	if err := client.insert(ctx, []IngestBundle{{Sample: SampleRow{TS: start, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}}, Procs: procs}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	mv := client.table("procs_top_1m_mv")
	if err := client.conn.Exec(ctx, "SYSTEM REFRESH VIEW "+mv); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := client.conn.Exec(ctx, "SYSTEM WAIT VIEW "+mv); err != nil {
		t.Fatalf("wait: %v", err)
	}

	api := ProcStartKey{NodeID: node, Key: wire.NewProcessKey("api", "/bin/api")}
	nostart := ProcStartKey{NodeID: node, Key: wire.NewProcessKey("nostart", "/bin/nostart")}
	missing := ProcStartKey{NodeID: node, Key: wire.NewProcessKey("ghost", "/bin/ghost")}
	other := ProcStartKey{NodeID: uuid.New(), Key: api.Key}
	keys := []ProcStartKey{api, nostart, missing, other}

	got, err := client.ProcessStarts(ctx, org, keys, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[api].PID != 11 || got[api].StartTS == nil || !got[api].StartTS.Equal(born) || got[nostart].StartTS != nil {
		t.Fatalf("starts = %+v", got)
	}
	// An earlier time sees the older pid.
	got, err = client.ProcessStarts(ctx, org, []ProcStartKey{api}, start.Add(30*time.Second))
	if err != nil || got[api].PID != 10 {
		t.Fatalf("starts at an earlier time = %+v, %v", got, err)
	}

	seen, err := client.FirstSeen(ctx, org, keys, start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || !seen[api].Equal(start) || !seen[nostart].Equal(start) {
		t.Fatalf("first seen = %+v, want %v", seen, start)
	}
}
