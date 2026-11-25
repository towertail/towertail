package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/store"
)

func (e *env) propose(t *testing.T, c Change) Proposal {
	t.Helper()
	p := Proposal{ID: uuid.New(), OrgID: e.actor.OrgID, ChatID: e.chat, Change: c, Status: ProposalPending, CreatedAt: e.now}
	if err := e.st.SaveProposal(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func twoRows(kind string, role store.Role) Change {
	return Change{Tool: "set_thresholds", Kind: kind, MinRole: role, Backup: kind == "rules", Rows: []ChangeRow{
		{ID: "r1", Target: "web-1", Field: "cpuWarn", Old: "80", New: "97", Checked: true},
		{ID: "r2", Target: "web-1", Field: "cpuCrit", Old: "90", New: "99", Checked: true},
	}}
}

func TestApplyProposal(t *testing.T) {
	e := newEnv(t, config.AssistantConfig{})
	p := e.propose(t, twoRows("rules", store.RoleAdmin))
	got, err := e.svc.ApplyProposal(context.Background(), e.actor, p.ID, []string{"r2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ProposalApplied || got.AppliedBy != e.actor.UserID || got.AppliedAt.IsZero() || got.Applied.BackupID == uuid.Nil {
		t.Fatalf("proposal %+v", got)
	}
	if e.writer.applies != 1 || len(e.writer.rows) != 1 || e.writer.rows[0] != "r2" {
		t.Fatalf("writer %+v", e.writer)
	}
	if got.Rows[0].Checked || !got.Rows[1].Checked {
		t.Fatalf("rows %+v", got.Rows)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["backupId"] != got.Applied.BackupID.String() || m["status"] != "applied" || m["tool"] != "set_thresholds" || m["appliedAt"] == nil {
		t.Fatalf("json %s", b)
	}
	if _, err := e.svc.ApplyProposal(context.Background(), e.actor, p.ID, []string{"r1"}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second apply: %v", err)
	}
	if e.writer.applies != 1 {
		t.Fatal("second apply reached the writer")
	}
}

func TestApplyProposalChecks(t *testing.T) {
	ctx := context.Background()
	t.Run("role", func(t *testing.T) {
		e := newEnv(t, config.AssistantConfig{})
		e.actor.Role = store.RoleMember
		p := e.propose(t, twoRows("rules", store.RoleAdmin))
		if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); !errors.Is(err, ErrRole) {
			t.Fatalf("err %v", err)
		}
		e.actor.Role = store.RoleViewer
		p = e.propose(t, twoRows("alerts", store.RoleMember))
		if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); !errors.Is(err, ErrRole) {
			t.Fatalf("viewer err %v", err)
		}
	})
	t.Run("member applies alerts", func(t *testing.T) {
		e := newEnv(t, config.AssistantConfig{})
		e.actor.Role = store.RoleMember
		p := e.propose(t, twoRows("alerts", store.RoleMember))
		if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ai changes", func(t *testing.T) {
		e := newEnv(t, config.AssistantConfig{})
		e.settings.ai = store.AIChangesAlerts
		p := e.propose(t, twoRows("rules", store.RoleAdmin))
		if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); !errors.Is(err, ErrAIChanges) {
			t.Fatalf("err %v", err)
		}
		e.settings.ai = store.AIChangesOff
		p = e.propose(t, twoRows("alerts", store.RoleMember))
		if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); !errors.Is(err, ErrAIChanges) {
			t.Fatalf("off err %v", err)
		}
		if e.writer.applies != 0 {
			t.Fatal("writer called")
		}
	})
	t.Run("rows", func(t *testing.T) {
		e := newEnv(t, config.AssistantConfig{})
		p := e.propose(t, twoRows("rules", store.RoleAdmin))
		for _, rows := range [][]string{nil, {}, {"r9"}, {"r1", "r9"}} {
			if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, rows); !errors.Is(err, ErrBadRows) {
				t.Fatalf("rows %v: err %v", rows, err)
			}
		}
	})
	t.Run("expired", func(t *testing.T) {
		e := newEnv(t, config.AssistantConfig{})
		p := e.propose(t, twoRows("rules", store.RoleAdmin))
		e.now = e.now.Add(25 * time.Hour)
		if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); !errors.Is(err, ErrExpired) {
			t.Fatalf("err %v", err)
		}
		got, _ := e.st.Proposal(ctx, e.actor.OrgID, p.ID)
		if got.Status != ProposalExpired {
			t.Fatalf("status %s", got.Status)
		}
		if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); !errors.Is(err, ErrExpired) {
			t.Fatalf("again: %v", err)
		}
	})
	t.Run("other org and user", func(t *testing.T) {
		e := newEnv(t, config.AssistantConfig{})
		p := e.propose(t, twoRows("rules", store.RoleAdmin))
		other := e.actor
		other.OrgID = uuid.New()
		if _, err := e.svc.ApplyProposal(ctx, other, p.ID, []string{"r1"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("org: %v", err)
		}
		other = e.actor
		other.UserID = uuid.New()
		if _, err := e.svc.ApplyProposal(ctx, other, p.ID, []string{"r1"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("user: %v", err)
		}
		if _, err := e.svc.ApplyProposal(ctx, e.actor, uuid.New(), []string{"r1"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown: %v", err)
		}
	})
	t.Run("writer error keeps pending", func(t *testing.T) {
		e := newEnv(t, config.AssistantConfig{})
		e.writer.applyErr = errors.New("conflict")
		p := e.propose(t, twoRows("rules", store.RoleAdmin))
		if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); err == nil {
			t.Fatal("want error")
		}
		got, _ := e.st.Proposal(ctx, e.actor.OrgID, p.ID)
		if got.Status != ProposalPending {
			t.Fatalf("status %s", got.Status)
		}
	})
}

func TestUndoProposal(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, config.AssistantConfig{})
	p := e.propose(t, twoRows("rules", store.RoleAdmin))
	if _, err := e.svc.UndoProposal(ctx, e.actor, p.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("undo before apply: %v", err)
	}
	if _, err := e.svc.ApplyProposal(ctx, e.actor, p.ID, []string{"r1"}); err != nil {
		t.Fatal(err)
	}

	conflict := errors.New("conflict: rules changed")
	e.writer.undoErr = conflict
	if _, err := e.svc.UndoProposal(ctx, e.actor, p.ID); !errors.Is(err, conflict) {
		t.Fatalf("conflict: %v", err)
	}
	if got, _ := e.st.Proposal(ctx, e.actor.OrgID, p.ID); got.Status != ProposalApplied {
		t.Fatalf("status after conflict %s", got.Status)
	}

	e.writer.undoErr = nil
	e.settings.ai = store.AIChangesAlerts
	if _, err := e.svc.UndoProposal(ctx, e.actor, p.ID); !errors.Is(err, ErrAIChanges) {
		t.Fatalf("ai rule: %v", err)
	}
	e.settings.ai = store.AIChangesAlertsAndRules
	e.actor.Role = store.RoleMember
	if _, err := e.svc.UndoProposal(ctx, e.actor, p.ID); !errors.Is(err, ErrRole) {
		t.Fatalf("role: %v", err)
	}
	e.actor.Role = store.RoleAdmin
	got, err := e.svc.UndoProposal(ctx, e.actor, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ProposalUndone || got.UndoneAt.IsZero() {
		t.Fatalf("proposal %+v", got)
	}
	if _, err := e.svc.UndoProposal(ctx, e.actor, p.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second undo: %v", err)
	}
}
