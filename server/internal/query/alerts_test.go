package query

import (
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/internal/clickhouse"
	"github.com/towertail/towertail/server/wire"
)

func TestAlertFromRowProcKey(t *testing.T) {
	a := AlertFromRow(clickhouse.AlertRow{
		EventID: uuid.New(), Kind: "long_process",
		Payload: `{"pid":7,"name":"node","cmd":"node server.js","cpu_pct":88}`,
	}, clickhouse.AlertMark{})
	if a.Proc == nil {
		t.Fatal("no proc")
	}
	if want := wire.NewProcessKey("node", "node server.js"); a.Proc.Key != want {
		t.Fatalf("key = %+v, want %+v", a.Proc.Key, want)
	}
}
