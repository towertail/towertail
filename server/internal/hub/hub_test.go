package hub

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/wire"
)

func TestPublishDiskScanOnlyToClientsThatSeeTheNode(t *testing.T) {
	h := New(slog.Default())
	org, other := uuid.New(), uuid.New()
	node, node2 := uuid.New(), uuid.New()

	sees := h.Register(org, nil)
	sees.setSubset([]uuid.UUID{node})
	all := h.Register(org, nil)
	notSub := h.Register(org, nil)
	notSub.setSubset([]uuid.UUID{node2})
	otherOrg := h.Register(other, nil)

	h.PublishDiskScan(org, wire.DiskScanJob{ID: uuid.New(), NodeID: node, State: "running"})

	for name, c := range map[string]*Client{"subscribed": sees, "unfiltered": all} {
		select {
		case data := <-c.send:
			var msg wire.WSMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type != "disk_scan_updated" || msg.NodeID == nil || *msg.NodeID != node || msg.Job == nil || msg.Job.State != "running" {
				t.Errorf("%s: bad frame %s", name, data)
			}
		default:
			t.Errorf("%s: no frame", name)
		}
	}
	for name, c := range map[string]*Client{"other node": notSub, "other org": otherOrg} {
		select {
		case data := <-c.send:
			t.Errorf("%s: unexpected frame %s", name, data)
		default:
		}
	}
}
