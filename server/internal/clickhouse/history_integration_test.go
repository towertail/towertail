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
				ProcsTotal: uint32(100 + i%7), Errors: []string{},
			}})
		}
	}
	if err := client.insert(ctx, bundles); err != nil {
		t.Fatalf("insert: %v", err)
	}

	all := []string{"cpu", "mem", "net_rx", "net_tx", "disk_read", "disk_write"}
	for _, tc := range []struct {
		name    string
		from    time.Time
		step    time.Duration
		metrics []string
	}{
		{"raw", end.Add(-time.Hour), 0, append([]string{"procs"}, all...)},
		{"raw by step", end.Add(-time.Hour), 5 * time.Minute, append([]string{"procs"}, all...)},
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
	if err != ErrNoRollup {
		t.Fatalf("procs on 1m tier: err = %v, want ErrNoRollup", err)
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
