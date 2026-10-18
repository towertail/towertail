package clickhouse

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"testing/fstest"
	"text/template"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// coreVersionTable is the goose version table of the core migrations.
const coreVersionTable = "goose_db_version"

// migrationSet is a folder of goose SQL files with its own version table.
type migrationSet struct {
	fsys  fs.FS
	table string
}

// AddMigrations adds a migration set that runs after the core set, with
// its own version table. Call it before Start.
func (c *Client) AddMigrations(fsys fs.FS, table string) {
	c.extra = append(c.extra, migrationSet{fsys: fsys, table: table})
}

// ApplyMigrations runs the core migrations, then the added sets. The SQL
// files use {{.DB}} for the database name.
func (c *Client) ApplyMigrations(ctx context.Context) error {
	if err := c.conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+c.db); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	db := ch.OpenDB(c.options())
	defer db.Close()

	if err := c.markBaseline(ctx, db); err != nil {
		return fmt.Errorf("mark baseline: %w", err)
	}
	core, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	sets := append([]migrationSet{{fsys: core, table: coreVersionTable}}, c.extra...)
	for _, set := range sets {
		if err := c.migrate(ctx, db, set); err != nil {
			return fmt.Errorf("%s: %w", set.table, err)
		}
	}
	return nil
}

func (c *Client) migrate(ctx context.Context, db *sql.DB, set migrationSet) error {
	fsys, err := renderMigrations(set.fsys, c.db)
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectClickHouse, db, fsys,
		goose.WithTableName(c.table(set.table)),
		goose.WithDisableGlobalRegistry(true),
		goose.WithIsolateDDL(true),
	)
	if err != nil {
		return err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return err
	}
	for _, r := range results {
		c.log.Info("clickhouse: migration applied", "table", set.table, "version", r.Source.Version, "dur_ms", r.Duration.Milliseconds())
	}
	return nil
}

// markBaseline marks version 1 as applied for a database with the schema
// from before goose and no version table.
func (c *Client) markBaseline(ctx context.Context, db *sql.DB) error {
	exists := func(table string) (bool, error) {
		var n uint8
		err := c.conn.QueryRow(ctx, "EXISTS TABLE "+c.table(table)).Scan(&n)
		return n == 1, err
	}
	hasVersion, err := exists(coreVersionTable)
	if err != nil || hasVersion {
		return err
	}
	hasSchema, err := exists("samples_raw")
	if err != nil || !hasSchema {
		return err
	}
	st, err := database.NewStore(database.DialectClickHouse, c.table(coreVersionTable))
	if err != nil {
		return err
	}
	if err := st.CreateVersionTable(ctx, db); err != nil {
		return err
	}
	for _, v := range []int64{0, 1} {
		if err := st.Insert(ctx, db, database.InsertRequest{Version: v}); err != nil {
			return err
		}
	}
	c.log.Info("clickhouse: baseline marked as applied", "database", c.db)
	return nil
}

// renderMigrations fills {{.DB}} in each SQL file of fsys.
func renderMigrations(fsys fs.FS, db string) (fs.FS, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	out := fstest.MapFS{}
	for _, name := range names {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		t, err := template.New(name).Option("missingkey=error").Parse(string(body))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		var b bytes.Buffer
		if err := t.Execute(&b, struct{ DB string }{DB: db}); err != nil {
			return nil, fmt.Errorf("render %s: %w", name, err)
		}
		out[name] = &fstest.MapFile{Data: b.Bytes()}
	}
	return out, nil
}
