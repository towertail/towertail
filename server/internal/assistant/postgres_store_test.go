//go:build integration

package assistant

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/store"
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
	return config.PostgresConfig{
		DSN:      fmt.Sprintf("postgres://postgres@%s:%s/towertail?sslmode=disable", host, port.Port()),
		Password: "test",
	}
}

func TestPostgresStoreContract(t *testing.T) {
	ctx := context.Background()
	// OpenPostgres runs the migrations, so this also runs 00010 up on an empty database.
	s, err := store.OpenPostgres(ctx, startPostgres(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	testStore(t, storeHarness{
		st: NewPostgresStore(s.Pool()),
		newOrg: func(t *testing.T) uuid.UUID {
			id := uuid.New()
			if _, err := s.Pool().Exec(ctx, `INSERT INTO orgs (id, name) VALUES ($1, $2)`, id, t.Name()); err != nil {
				t.Fatal(err)
			}
			return id
		},
	})
}
