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

	"github.com/towertail/server/internal/config"
)

// startClickHouse boots a throwaway ClickHouse container and returns a
// connected, migrated Client plus a cleanup func.
func startClickHouse(t *testing.T) (*Client, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	container, err := tcch.Run(ctx,
		"clickhouse/clickhouse-server:24.3-alpine",
		tcch.WithUsername("towertail"),
		tcch.WithPassword("test"),
		tcch.WithDatabase("towertail"),
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

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client := New(config.ClickHouseConfig{
		Addr:             host + ":" + port.Port(),
		Database:         "towertail",
		User:             "towertail",
		Password:         "test",
		DialTimeout:      10 * time.Second,
		MaxOpenConns:     4,
		MaxIdleConns:     2,
		ConnMaxLifetime:  time.Minute,
		BatchFlushRows:   100,
		BatchFlushBytes:  1 << 20,
		BatchFlushPeriod: 200 * time.Millisecond,
		RetentionDays:    7,
	}, log)

	startCtx, startCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer startCancel()
	if err := client.Start(startCtx); err != nil {
		_ = container.Terminate(context.Background())
		t.Fatalf("client start: %v", err)
	}

	cleanup := func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = client.Stop(stopCtx)
		termCtx, termCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer termCancel()
		_ = tc.TerminateContainer(container, tc.StopTimeout(5*time.Second))
		_ = termCtx
	}
	return client, cleanup
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

	ok := client.Batcher().Enqueue(IngestBundle{
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
	if !ok {
		t.Fatalf("Enqueue returned false — queue full?")
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
