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
	"testing/fstest"
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
	client := startClient(t, addr, "towertail", nil)
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

// startClient starts a Client on database db. setup runs before Start.
func startClient(t *testing.T, addr, db string, setup func(*Client)) *Client {
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
	if setup != nil {
		setup(client)
	}
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

	raw := startClient(t, addr, "old", nil)
	ctx := context.Background()
	if err := raw.conn.Exec(ctx, "DROP TABLE "+raw.table(coreVersionTable)); err != nil {
		t.Fatalf("drop version table: %v", err)
	}
	stopClient(raw)

	client := startClient(t, addr, "old", nil)
	defer stopClient(client)
	got := versions(t, client, coreVersionTable)
	if len(got) < 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("versions = %v, want 0, 1, ...", got)
	}
}

// Another database name and an added migration set work side by side
// with the default database.
func TestMigrationsOtherDatabaseAndExtraSet(t *testing.T) {
	addr, stop := startContainer(t)
	defer stop()

	extra := fstest.MapFS{
		"00001_cloud.sql": {Data: []byte(`-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE IF NOT EXISTS {{.DB}}.cloud_usage (org_id UUID, n UInt64)
ENGINE = MergeTree ORDER BY org_id;
`)},
	}
	client := startClient(t, addr, "tt_other", func(c *Client) {
		c.AddMigrations(extra, "cloud_db_version")
	})
	defer stopClient(client)

	var n uint8
	ctx := context.Background()
	if err := client.conn.QueryRow(ctx, "EXISTS TABLE tt_other.cloud_usage").Scan(&n); err != nil || n != 1 {
		t.Fatalf("cloud_usage exists = %d, err %v", n, err)
	}
	if err := client.conn.QueryRow(ctx, "EXISTS TABLE tt_other.samples_raw").Scan(&n); err != nil || n != 1 {
		t.Fatalf("samples_raw exists = %d, err %v", n, err)
	}
	if err := client.conn.QueryRow(ctx, "EXISTS DATABASE towertail").Scan(&n); err != nil || n != 0 {
		t.Fatalf("towertail database exists = %d, err %v", n, err)
	}
	if got := versions(t, client, "cloud_db_version"); len(got) == 0 || got[len(got)-1] != 1 {
		t.Fatalf("cloud versions = %v", got)
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
	want := []string{"samples_raw", "disks", "disk_io_devices", "procs_topn", "events"}
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
		},
		Disks: []DiskRow{
			{TS: ts, OrgID: orgID, NodeID: nodeID, Mount: "/", FS: "ext4", Used: 100, Total: 1000},
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
