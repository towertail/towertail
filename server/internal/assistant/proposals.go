package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

// Proposal statuses.
const (
	ProposalPending = "pending"
	ProposalApplied = "applied"
	ProposalUndone  = "undone"
	ProposalExpired = "expired"
	// ProposalApplying and ProposalUndoing hold the claim of one server
	// while the writer runs.
	ProposalApplying = "applying"
	ProposalUndoing  = "undoing"
)

// proposalTTL is how long a pending proposal stays valid.
const proposalTTL = 24 * time.Hour

// Errors of ApplyProposal and UndoProposal.
var (
	ErrNotPending = errors.New("assistant: proposal is not in the right state")
	ErrExpired    = errors.New("assistant: proposal expired")
	ErrRole       = errors.New("assistant: role too low for this change")
	ErrAIChanges  = errors.New("assistant: changes by AI do not allow this change")
	ErrBadRows    = errors.New("assistant: rows must be a non-empty subset of the proposal rows")
)

// Change is what a write tool wants to do.
type Change struct {
	Tool    string          `json:"tool"`
	Kind    string          `json:"kind"`    // "alerts" | "rules"
	MinRole store.Role      `json:"minRole"` // member for alerts, admin for rules
	Backup  bool            `json:"backup"`
	Rows    []ChangeRow     `json:"rows"`
	Impact  *Impact         `json:"impact,omitempty"`
	Input   json.RawMessage `json:"input"`
}

// ChangeRow is one line of a change that the user can check or uncheck.
type ChangeRow struct {
	ID      string `json:"id"`
	Target  string `json:"target"`
	Field   string `json:"field"`
	Old     string `json:"old"`
	New     string `json:"new"`
	Checked bool   `json:"checked"`
}

// Impact is the effect of a change on the past.
type Impact struct {
	Before int    `json:"before"`
	After  int    `json:"after"`
	Window string `json:"window"`
	Note   string `json:"note,omitempty"`
}

// Applied is the result of Writer.Apply.
type Applied struct {
	BackupID uuid.UUID       `json:"backupId,omitzero"` // rules changes
	Inverse  json.RawMessage `json:"inverse,omitempty"` // the undo call of an alert action
	Rows     []string        `json:"rows"`
}

// Proposal is a stored change. Its JSON is the proposal event and the
// proposal of the apply and undo routes.
type Proposal struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"-"`
	ChatID    uuid.UUID `json:"chatId"`
	MessageID uuid.UUID `json:"messageId,omitzero"`
	Change
	Status    string    `json:"status"`
	Applied   *Applied  `json:"applied,omitempty"`
	AppliedBy uuid.UUID `json:"-"`
	AppliedAt time.Time `json:"appliedAt,omitzero"`
	UndoneAt  time.Time `json:"undoneAt,omitzero"`
	CreatedAt time.Time `json:"createdAt"`
}

// MarshalJSON adds backupId after an apply.
func (p Proposal) MarshalJSON() ([]byte, error) {
	type plain Proposal
	v := struct {
		plain
		BackupID uuid.UUID `json:"backupId,omitzero"`
	}{plain: plain(p)}
	if p.Applied != nil {
		v.BackupID = p.Applied.BackupID
	}
	return json.Marshal(v)
}

// Expired tells if a pending proposal is older than 24 h.
func (p Proposal) Expired(now time.Time) bool {
	return p.Status == ProposalExpired || (p.Status == ProposalPending && now.Sub(p.CreatedAt) > proposalTTL)
}

// permits tells if the org rule lets a change of this kind go through.
func permits(ai store.AIChanges, kind string) bool {
	switch kind {
	case "alerts":
		return ai == store.AIChangesAlerts || ai == store.AIChangesAlertsAndRules
	case "rules":
		return ai == store.AIChangesAlertsAndRules
	}
	return false
}

// load gives the proposal if it belongs to the org and to a chat of the user.
func (s *Service) load(ctx context.Context, a Actor, id uuid.UUID) (Proposal, error) {
	p, err := s.store.Proposal(ctx, a.OrgID, id)
	if err != nil {
		return Proposal{}, err
	}
	if _, _, err := s.store.Chat(ctx, a.OrgID, a.UserID, p.ChatID); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// check runs conditions 3 and 4: the role and the org rule.
func (s *Service) check(ctx context.Context, a Actor, c Change) error {
	if !a.Role.Allows(c.MinRole) {
		return ErrRole
	}
	_, ai, err := s.orgSettings(ctx, a.OrgID)
	if err != nil {
		return err
	}
	if !permits(ai, c.Kind) {
		return ErrAIChanges
	}
	return nil
}

// ApplyProposal applies the checked rows of a pending proposal. Two
// applies of one proposal in this process run one after the other. The
// store claim stops two servers from applying one proposal.
func (s *Service) ApplyProposal(ctx context.Context, a Actor, id uuid.UUID, rows []string) (Proposal, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	p, err := s.load(ctx, a, id)
	if err != nil {
		return Proposal{}, err
	}
	now := s.now()
	switch {
	case p.Expired(now):
		if p.Status == ProposalPending {
			p.Status = ProposalExpired
			if err := s.store.UpdateProposal(ctx, p, ProposalPending); err != nil && !errors.Is(err, ErrNotPending) {
				return Proposal{}, err
			}
		}
		return Proposal{}, ErrExpired
	case p.Status != ProposalPending:
		return Proposal{}, ErrNotPending
	}
	if err := s.check(ctx, a, p.Change); err != nil {
		return Proposal{}, err
	}
	if len(rows) == 0 || !subset(rows, p.Rows) {
		return Proposal{}, ErrBadRows
	}
	if s.writer == nil {
		return Proposal{}, errors.New("assistant: no writer")
	}
	orig := p
	p.Rows = slices.Clone(p.Rows)
	for i := range p.Rows {
		p.Rows[i].Checked = slices.Contains(rows, p.Rows[i].ID)
	}
	claim := p
	claim.Status = ProposalApplying
	if err := s.store.UpdateProposal(ctx, claim, ProposalPending); err != nil {
		return Proposal{}, err
	}
	ap, err := s.writer.Apply(a.ctx(ctx), a, p.Change, rows)
	done := context.WithoutCancel(ctx)
	if err != nil {
		_ = s.store.UpdateProposal(done, orig, ProposalApplying)
		return Proposal{}, err
	}
	p.Status, p.Applied, p.AppliedBy, p.AppliedAt = ProposalApplied, &ap, a.UserID, now
	if err := s.store.UpdateProposal(done, p, ProposalApplying); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// UndoProposal undoes an applied proposal. An error of Writer.Undo comes
// back unchanged and the proposal stays applied.
func (s *Service) UndoProposal(ctx context.Context, a Actor, id uuid.UUID) (Proposal, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	p, err := s.load(ctx, a, id)
	if err != nil {
		return Proposal{}, err
	}
	if p.Status != ProposalApplied || p.Applied == nil {
		return Proposal{}, ErrNotPending
	}
	if err := s.check(ctx, a, p.Change); err != nil {
		return Proposal{}, err
	}
	if s.writer == nil {
		return Proposal{}, errors.New("assistant: no writer")
	}
	orig := p
	claim := p
	claim.Status = ProposalUndoing
	if err := s.store.UpdateProposal(ctx, claim, ProposalApplied); err != nil {
		return Proposal{}, err
	}
	err = s.writer.Undo(a.ctx(ctx), a, p.Change, *p.Applied)
	done := context.WithoutCancel(ctx)
	if err != nil {
		_ = s.store.UpdateProposal(done, orig, ProposalUndoing)
		return Proposal{}, err
	}
	p.Status, p.UndoneAt = ProposalUndone, s.now()
	if err := s.store.UpdateProposal(done, p, ProposalUndoing); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

func subset(ids []string, rows []ChangeRow) bool {
	return !slices.ContainsFunc(ids, func(id string) bool {
		return !slices.ContainsFunc(rows, func(r ChangeRow) bool { return r.ID == id })
	})
}
