//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/towertail/towertail/server/config"
)

// pgImage is the Postgres of deploy/docker-compose.yml in the cloud repo.
const pgImage = "postgres:17.11-alpine"

func startPostgres(t *testing.T) config.PostgresConfig {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        pgImage,
			Env:          map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "towertail"},
			ExposedPorts: []string{"5432/tcp"},
			// The entrypoint starts Postgres twice. The second start is the real one.
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return config.PostgresConfig{DSN: fmt.Sprintf("postgres://postgres@%s:%s/towertail?sslmode=disable", host, port.Port())}
}

func TestPostgresContract(t *testing.T) {
	cfg := startPostgres(t)
	ctx := context.Background()

	// The DSN has no password. It comes from the config.
	cfg.Password = "test"
	s, err := OpenPostgres(ctx, cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// A second open runs no migration again.
	again, err := OpenPostgres(ctx, cfg)
	if err != nil {
		t.Fatalf("open again: %v", err)
	}
	_ = again.Close()

	newOrg := func(t *testing.T) uuid.UUID {
		id := uuid.New()
		if _, err := s.Pool().Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1, $2)`, id, t.Name()); err != nil {
			t.Fatal(err)
		}
		return id
	}
	testContract(t, storeHarness{s: s, newOrg: newOrg})

	t.Run("ZeroOrgExists", func(t *testing.T) {
		tok := Token{ID: uuid.New(), OrgID: ZeroOrg, Kind: TokenKindAdmin, Hash: []byte(uuid.NewString())}
		if err := s.PutToken(ctx, tok); err != nil {
			t.Errorf("token in ZeroOrg: %v", err)
		}
	})
	t.Run("UnknownOrg", func(t *testing.T) {
		if err := s.PutNode(ctx, uuid.New(), Node{ID: uuid.New()}); err == nil {
			t.Error("node in an org that does not exist: want error")
		}
	})
	t.Run("NotFoundIsNotWrapped", func(t *testing.T) {
		if _, err := s.GetTokenByHash(ctx, []byte("none")); !errors.Is(err, ErrNotFound) {
			t.Errorf("got %v", err)
		}
	})
}
