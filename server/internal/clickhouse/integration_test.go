//go:build integration

// Real ClickHouse-backed tests for the Client/batcher. Spun up via
// testcontainers-go so the tests are hermetic — one container per test
// process. Build tag `integration` keeps them out of the default `go
// test ./...` run; run with:
//
//	go test -tags=integration ./internal/clickhouse/...
//
// Requires a working Docker daemon. ~5s overhead per test process for
// container boot.
package clickhouse

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	tc "github.com/testcontainers/testcontainers-go"
	tcch "github.com/testcontainers/testcontainers-go/modules/clickhouse"

	"github.com/towertail/towertail/server/config"
)

const testImage = "clickhouse/clickhouse-server:26.8.26.12-alpine"

// startClickHouse boots a throwaway ClickHouse container and returns a
// connected, migrated Client plus a cleanup func.
func startClickHouse(t *testing.T) (*Client, func()) {
	t.Helper()
	addr, stop := startContainer(t)
	client := startClient(t, addr, "towertail")
	return client, func() {
		stopClient(client)
		stop()
	}
}

// startContainer boots a ClickHouse container and returns its native
// address and a stop func.
func startContainer(t *testing.T) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	container, err := tcch.Run(ctx,
		testImage,
		tcch.WithUsername("towertail"),
		tcch.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start clickhouse: %v", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return host + ":" + port.Port(), func() {
		_ = tc.TerminateContainer(container, tc.StopTimeout(5*time.Second))
	}
}

// startClient starts a Client on database db.
func startClient(t *testing.T, addr, db string) *Client {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client := New(config.ClickHouseConfig{
		Addr:             addr,
		Database:         db,
		User:             "towertail",
		Password:         "test",
		DialTimeout:      10 * time.Second,
		MaxOpenConns:     4,
		MaxIdleConns:     2,
		ConnMaxLifetime:  time.Minute,
		BatchFlushRows:   100,
		BatchFlushBytes:  1 << 20,
		BatchFlushPeriod: 200 * time.Millisecond,
		SpillDir:         t.TempDir(),
		SpillMaxBytes:    1 << 20,
	}, log)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("client start: %v", err)
	}
	return client
}

func stopClient(c *Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Stop(ctx)
}

// versions gives the applied goose versions in table, in order.
func versions(t *testing.T, c *Client, table string) []int64 {
	t.Helper()
	rows, err := c.conn.Query(context.Background(),
		"SELECT version_id FROM "+c.table(table)+" WHERE is_applied ORDER BY version_id")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func TestMigrationsNewDatabase(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	got := versions(t, client, coreVersionTable)
	if len(got) == 0 || got[len(got)-1] < 1 {
		t.Fatalf("versions = %v, want 1 or more", got)
	}
}

// A database from before goose has the tables and no version table. Start
// marks the baseline and does not run it again.
func TestMigrationsMarkBaseline(t *testing.T) {
	addr, stop := startContainer(t)
	defer stop()

	raw := startClient(t, addr, "old")
	ctx := context.Background()
	org, node := uuid.New(), uuid.New()
	ts := time.Now().UTC().Add(-3 * time.Hour)
	if err := raw.insert(ctx, []IngestBundle{
		{Sample: SampleRow{TS: ts, OrgID: org, NodeID: node, SchemaV: 1, CPUPct: 20, Errors: []string{}}},
		{Sample: SampleRow{TS: ts.Add(time.Second), OrgID: org, NodeID: node, SchemaV: 1, CPUPct: 60, Errors: []string{}}},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Make the schema from before goose: drop the version table and what
	// the later migrations add.
	for _, name := range []string{coreVersionTable, "samples_1h_mv", "samples_1h", "procs_top_1m_mv", "procs_top_1m", "procs_top_1h_mv", "procs_top_1h"} {
		if err := raw.conn.Exec(ctx, "DROP TABLE "+raw.table(name)); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	stopClient(raw)

	client := startClient(t, addr, "old")
	defer stopClient(client)
	got := versions(t, client, coreVersionTable)
	if len(got) < 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("versions = %v, want 0, 1, 2, ...", got)
	}
	// samples_1h gets the old rows from samples_5m.
	pts, _, err := client.QueryHistory(ctx, HistoryQuery{OrgID: org, NodeID: node, Metric: "cpu", From: ts.Add(-60 * 24 * time.Hour), To: ts.Add(time.Hour)})
	if err != nil || len(pts) != 1 || pts[0].Value != 40 || pts[0].Max != 60 {
		t.Fatalf("samples_1h = %+v, err %v", pts, err)
	}
}

// Another database name works side by side with the default database.
func TestMigrationsOtherDatabase(t *testing.T) {
	addr, stop := startContainer(t)
	defer stop()

	client := startClient(t, addr, "tt_other")
	defer stopClient(client)

	var n uint8
	ctx := context.Background()
	if err := client.conn.QueryRow(ctx, "EXISTS TABLE tt_other.samples_raw").Scan(&n); err != nil || n != 1 {
		t.Fatalf("samples_raw exists = %d, err %v", n, err)
	}
	if err := client.conn.QueryRow(ctx, "EXISTS DATABASE towertail").Scan(&n); err != nil || n != 0 {
		t.Fatalf("towertail database exists = %d, err %v", n, err)
	}

	// Ingest goes to the other database.
	nodeID := uuid.New()
	if err := client.Batcher().Enqueue(IngestBundle{Sample: SampleRow{
		TS: time.Now().UTC(), OrgID: uuid.New(), NodeID: nodeID, SchemaV: 1, Errors: []string{},
	}}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitCount(t, client, "SELECT count() FROM tt_other.samples_raw WHERE node_id = ?", nodeID, 1)
}

func waitCount(t *testing.T, c *Client, q string, arg any, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var count uint64
	for time.Now().Before(deadline) {
		if err := c.conn.QueryRow(context.Background(), q, arg).Scan(&count); err == nil && count >= want {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if count != want {
		t.Fatalf("%s: count = %d, want %d", q, count, want)
	}
}

func TestMigrationsCreateExpectedTables(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()

	ctx := context.Background()
	rows, err := client.conn.Query(ctx, "SHOW TABLES FROM towertail")
	if err != nil {
		t.Fatalf("show tables: %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[name] = true
	}
	want := []string{"samples_raw", "disks", "disk_io_devices", "procs_topn", "events",
		"samples_1m", "samples_5m", "samples_1h", "procs_top_1m", "procs_top_1h", "proc_ports", "health_1m", "health_5m", "health_1h"}
	for _, tbl := range want {
		if !seen[tbl] {
			t.Errorf("expected table %q missing; got %v", tbl, seen)
		}
	}
}

func TestBatcherFlushesAndRoundtripsSamples(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()

	orgID := uuid.New()
	nodeID := uuid.New()
	ts := time.Now().UTC().Truncate(time.Second)

	err := client.Batcher().Enqueue(IngestBundle{
		Sample: SampleRow{
			TS:          ts,
			OrgID:       orgID,
			NodeID:      nodeID,
			SchemaV:     1,
			HostName:    "int-test",
			HostOS:      "linux",
			HostArch:    "arm64",
			HostKernel:  "6.6",
			HostUptimeS: 123,
			HostSampler: "test/0.1",
			CPUPct:      42.0,
			CPUCores:    4,
			MemUsed:     1000,
			MemTotal:    2000,
			ProcsTotal:  10,
			ProcsRoot:   0,
			Errors:      []string{},
			Zombies:     u32(3), PidsUsed: u64(40), PidsMax: u64(80), FilesUsed: u64(5), FilesMax: u64(50),
			PSICPUSome: f32(1.5), PSIMemSome: f32(2.5), PSIMemFull: f32(3.5), PSIIOSome: f32(4.5), PSIIOFull: f32(5.5),
			MemPressurePct: u8(30), DiskRatioMax: f32(0.1), InodesRatioMax: f32(0.2),
		},
		Disks: []DiskRow{
			{TS: ts, OrgID: orgID, NodeID: nodeID, Mount: "/", FS: "ext4", Used: 100, Total: 1000, InodesUsed: u64(7), InodesTotal: u64(70)},
		},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// The batcher flushes every 200ms (BatchFlushPeriod). Give it a bit.
	deadline := time.Now().Add(5 * time.Second)
	var count uint64
	for time.Now().Before(deadline) {
		row := client.conn.QueryRow(context.Background(),
			"SELECT count() FROM towertail.samples_raw WHERE node_id = ?", nodeID)
		if err := row.Scan(&count); err == nil && count > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if count != 1 {
		t.Fatalf("expected 1 sample row, got %d", count)
	}

	// Disk row should also be present.
	deadline = time.Now().Add(2 * time.Second)
	var diskCount uint64
	for time.Now().Before(deadline) {
		row := client.conn.QueryRow(context.Background(),
			"SELECT count() FROM towertail.disks WHERE node_id = ?", nodeID)
		if err := row.Scan(&diskCount); err == nil && diskCount > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if diskCount != 1 {
		t.Fatalf("expected 1 disk row, got %d", diskCount)
	}

	var (
		zombies                    *uint32
		pidsUsed, pidsMax          *uint64
		psiCPU, psiIOFull, diskMax *float32
		pressure                   *uint8
		inodesUsed, inodesTotal    *uint64
	)
	ctx := context.Background()
	if err := client.conn.QueryRow(ctx, "SELECT zombies, pids_used, pids_max, psi_cpu_some, psi_io_full, mem_pressure_pct, disk_ratio_max FROM towertail.samples_raw WHERE node_id = ?", nodeID).
		Scan(&zombies, &pidsUsed, &pidsMax, &psiCPU, &psiIOFull, &pressure, &diskMax); err != nil {
		t.Fatal(err)
	}
	if zombies == nil || *zombies != 3 || *pidsUsed != 40 || *pidsMax != 80 || *psiCPU != 1.5 || *psiIOFull != 5.5 || *pressure != 30 || *diskMax != 0.1 {
		t.Errorf("health row = %v %v %v %v %v %v %v", zombies, pidsUsed, pidsMax, psiCPU, psiIOFull, pressure, diskMax)
	}
	if err := client.conn.QueryRow(ctx, "SELECT inodes_used, inodes_total FROM towertail.disks WHERE node_id = ?", nodeID).Scan(&inodesUsed, &inodesTotal); err != nil {
		t.Fatal(err)
	}
	if inodesUsed == nil || *inodesUsed != 7 || *inodesTotal != 70 {
		t.Errorf("inodes = %v %v", inodesUsed, inodesTotal)
	}
}

func TestBatcherFlushesPorts(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()
	ctx := context.Background()

	org, node := uuid.New(), uuid.New()
	ts := time.Now().UTC().Truncate(time.Millisecond)
	err := client.Batcher().Enqueue(IngestBundle{
		Sample: SampleRow{TS: ts, OrgID: org, NodeID: node, SchemaV: 1, Errors: []string{}},
		Ports: []PortRow{
			{TS: ts, OrgID: org, NodeID: node, PID: 10, Name: "nginx", User: "www", ListenTCP: []uint16{80, 443}, EstIn: 3, EstOut: 1},
			{TS: ts, OrgID: org, NodeID: node, PID: 11, Name: "dns", ListenUDP: []uint16{53}},
		},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var n uint64
	for time.Now().Before(deadline) {
		if err := client.conn.QueryRow(ctx, "SELECT count() FROM "+client.table("proc_ports")+" WHERE node_id = ?", node).Scan(&n); err == nil && n == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if n != 2 {
		t.Fatalf("proc_ports rows = %d, want 2", n)
	}
	var (
		tcp, udp []uint16
		user     string
		in, out  uint32
	)
	if err := client.conn.QueryRow(ctx, "SELECT listen_tcp, listen_udp, user_name, est_in, est_out FROM "+client.table("proc_ports")+
		" WHERE node_id = ? AND pid = 10", node).Scan(&tcp, &udp, &user, &in, &out); err != nil {
		t.Fatal(err)
	}
	if len(tcp) != 2 || tcp[1] != 443 || len(udp) != 0 || user != "www" || in != 3 || out != 1 {
		t.Fatalf("row = %v %v %q %d %d", tcp, udp, user, in, out)
	}
}

func TestWriteEventRoundtrip(t *testing.T) {
	client, cleanup := startClickHouse(t)
	defer cleanup()

	orgID := uuid.New()
	nodeID := uuid.New()
	eventID := uuid.New()
	ts := time.Now().UTC().Truncate(time.Second)

	if err := client.WriteEvent(context.Background(), EventRow{
		TS:      ts,
		OrgID:   orgID,
		NodeID:  nodeID,
		EventID: eventID,
		Kind:    "threshold_crossed",
		Metric:  "cpu",
		Tint:    "critical",
		Payload: "{\"value\":95.2}",
	}); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	var gotKind string
	row := client.conn.QueryRow(context.Background(),
		"SELECT kind FROM towertail.events WHERE event_id = ?", eventID)
	if err := row.Scan(&gotKind); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if gotKind != "threshold_crossed" {
		t.Errorf("kind mismatch: got %q", gotKind)
	}
}
