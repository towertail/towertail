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

// splitStatements breaks a file into individual statements at any `;`
// that lands outside a string/comment. ClickHouse's native-protocol
// query endpoint rejects multi-statement queries, so we send each one
// separately. Empty statements are dropped so trailing semicolons at
// end-of-file are harmless.
func splitStatements(body string) []string {
	// State: s=default, "='"=inside single-quoted literal,
	// "=`"=inside backtick identifier, "=-"=inside line comment,
	// "=*"=inside block comment.
	var out []string
	var cur strings.Builder
	state := 's'
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch state {
		case 's':
			switch {
			case c == ';':
				if s := strings.TrimSpace(cur.String()); s != "" {
					out = append(out, s)
				}
				cur.Reset()
			case c == '\'':
				cur.WriteByte(c)
				state = '\''
			case c == '`':
				cur.WriteByte(c)
				state = '`'
			case c == '-' && i+1 < len(body) && body[i+1] == '-':
				state = 'l'
				i++
			case c == '/' && i+1 < len(body) && body[i+1] == '*':
				state = 'b'
				i++
			default:
				cur.WriteByte(c)
			}
		case '\'':
			cur.WriteByte(c)
			if c == '\'' {
				state = 's'
			}
		case '`':
			cur.WriteByte(c)
			if c == '`' {
				state = 's'
			}
		case 'l':
			if c == '\n' {
				cur.WriteByte(c)
				state = 's'
			}
		case 'b':
			if c == '*' && i+1 < len(body) && body[i+1] == '/' {
				state = 's'
				i++
			}
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}
