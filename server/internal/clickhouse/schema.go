package clickhouse

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ApplyMigrations runs every embedded migration against the database.
// Migrations are idempotent (CREATE TABLE IF NOT EXISTS / CREATE
// MATERIALIZED VIEW IF NOT EXISTS) so boot-time replay is safe.
func (c *Client) ApplyMigrations(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		statements := splitStatements(string(body))
		for _, stmt := range statements {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if err := c.conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

// splitStatements breaks a file into individual statements on the
// delimiter `;` at the start of a line. Keeps multi-statement DDL
// (create table + create MV) in one file.
func splitStatements(body string) []string {
	var out []string
	lines := strings.Split(body, "\n")
	var cur strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == ";" {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		// Allow trailing `;` at the end of the file.
		s = strings.TrimSuffix(s, ";")
		out = append(out, s)
	}
	return out
}
