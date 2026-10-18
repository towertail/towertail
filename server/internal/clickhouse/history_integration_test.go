//go:build integration

package clickhouse

import (
	"context"
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

	top, err := client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: start, To: start.Add(3 * time.Minute)}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 || top[0].Name != "p30" || top[0].CPUPctAvg != 30 || top[0].RSSMax != 30<<20 || top[0].Cmd != "/bin/p30 --flag" {
		t.Fatalf("top from procs_topn = %+v", top)
	}
	top, err = client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: start.Add(-24 * time.Hour), To: start.Add(3 * time.Minute)}, 3)
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
	if err := client.conn.Exec(ctx, "INSERT INTO "+client.table("procs_top_1m")+" VALUES "+strings.Join(rows, ",")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	mv := client.table("procs_top_1h_mv")
	if err := client.conn.Exec(ctx, "SYSTEM REFRESH VIEW "+mv); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := client.conn.Exec(ctx, "SYSTEM WAIT VIEW "+mv); err != nil {
		t.Fatalf("wait: %v", err)
	}
	top, err := client.TopProcesses(ctx, RangeQuery{OrgID: org, NodeID: node, From: hour.Add(-30 * 24 * time.Hour), To: hour.Add(time.Hour)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 5 || top[0].Name != "p10" || math.Abs(top[0].CPUPctAvg-10) > 0.01 || top[4].Name != "p6" {
		t.Fatalf("top from procs_top_1h = %+v", top)
	}
}
