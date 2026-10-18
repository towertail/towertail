package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate runs the core migrations. The goose version table is
// goose_db_version.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	return MigrateFS(ctx, pool, sub, goose.DefaultTablename)
}

// MigrateFS runs the goose SQL files at the root of fsys, with table as
// the version table. The cloud uses it for its own sets.
func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, table string) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithTableName(table))
	if err != nil {
		return fmt.Errorf("postgres migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("postgres migrations: %w", err)
	}
	return nil
}
