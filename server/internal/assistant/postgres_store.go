package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the Store on Postgres. The tables come from the
// migration 00010_assistant.sql.
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore makes a store on pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// jsonOrNull gives nil (SQL NULL) for an empty value.
func jsonOrNull(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return nil, err
	}
	return b, nil
}

func nullUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *PostgresStore) CreateChat(ctx context.Context, c Chat) error {
	now := time.Now()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO assistant_chats (id, org_id, user_id, title, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		c.ID, c.OrgID, c.UserID, c.Title, cmp.Or(c.CreatedAt, now), cmp.Or(c.UpdatedAt, now))
	return err
}

func (s *PostgresStore) Chats(ctx context.Context, orgID, userID uuid.UUID, limit int) ([]Chat, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, org_id, user_id, title, created_at, updated_at FROM assistant_chats
		 WHERE org_id = $1 AND user_id = $2 ORDER BY updated_at DESC LIMIT NULLIF($3::int, 0)`,
		orgID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.OrgID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) Chat(ctx context.Context, orgID, userID, chatID uuid.UUID) (Chat, []StoredMessage, error) {
	var c Chat
	err := s.pool.QueryRow(ctx,
		`SELECT id, org_id, user_id, title, created_at, updated_at FROM assistant_chats
		 WHERE id = $1 AND org_id = $2 AND user_id = $3`, chatID, orgID, userID).
		Scan(&c.ID, &c.OrgID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, nil, ErrNotFound
	}
	if err != nil {
		return Chat{}, nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, chat_id, seq, role, native, parts, model, usage, status, created_at
		 FROM assistant_messages WHERE chat_id = $1 ORDER BY seq`, chatID)
	if err != nil {
		return Chat{}, nil, err
	}
	defer rows.Close()
	var msgs []StoredMessage
	for rows.Next() {
		var m StoredMessage
		var native, parts, usage []byte
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Seq, &m.Role, &native, &parts, &m.Model, &usage, &m.Status, &m.CreatedAt); err != nil {
			return Chat{}, nil, err
		}
		for _, f := range []struct {
			raw []byte
			dst any
		}{{native, &m.Native}, {parts, &m.Parts}, {usage, &m.Usage}} {
			if f.raw == nil {
				continue
			}
			if err := json.Unmarshal(f.raw, f.dst); err != nil {
				return Chat{}, nil, err
			}
		}
		msgs = append(msgs, m)
	}
	return c, msgs, rows.Err()
}

func (s *PostgresStore) DeleteChat(ctx context.Context, orgID, userID, chatID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM assistant_chats WHERE id = $1 AND org_id = $2 AND user_id = $3`, chatID, orgID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AppendMessage locks the chat row, so two appends get two Seq values.
func (s *PostgresStore) AppendMessage(ctx context.Context, m StoredMessage) error {
	native, err := jsonOrNull(m.Native)
	if err != nil {
		return err
	}
	parts, err := jsonOrNull(m.Parts)
	if err != nil {
		return err
	}
	usage, err := jsonOrNull(m.Usage)
	if err != nil {
		return err
	}
	at := cmp.Or(m.CreatedAt, time.Now())
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE assistant_chats SET updated_at = $2 WHERE id = $1`, m.ChatID, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO assistant_messages (id, chat_id, seq, role, native, parts, model, usage, status, created_at)
		 SELECT $1, $2, COALESCE(MAX(seq), 0) + 1, $3, $4, $5, $6, $7, $8, $9
		 FROM assistant_messages WHERE chat_id = $2`,
		m.ID, m.ChatID, m.Role, native, parts, m.Model, usage, m.Status, at)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) UpdateMessage(ctx context.Context, m StoredMessage) error {
	native, err := jsonOrNull(m.Native)
	if err != nil {
		return err
	}
	parts, err := jsonOrNull(m.Parts)
	if err != nil {
		return err
	}
	usage, err := jsonOrNull(m.Usage)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE assistant_messages SET role = $3, native = $4, parts = $5, model = $6, usage = $7, status = $8
		 WHERE id = $1 AND chat_id = $2`,
		m.ID, m.ChatID, m.Role, native, parts, m.Model, usage, m.Status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) SaveProposal(ctx context.Context, p Proposal) error {
	change, err := json.Marshal(p.Change)
	if err != nil {
		return err
	}
	applied, err := jsonOrNull(p.Applied)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO assistant_proposals
		   (id, org_id, chat_id, message_id, tool, change, status, applied, applied_by, applied_at, undone_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		p.ID, p.OrgID, p.ChatID, nullUUID(p.MessageID), p.Tool, change, p.Status, applied,
		nullUUID(p.AppliedBy), nullTime(p.AppliedAt), nullTime(p.UndoneAt), cmp.Or(p.CreatedAt, time.Now()))
	return err
}

const proposalCols = `id, org_id, chat_id, message_id, change, status, applied, applied_by, applied_at, undone_at, created_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanProposal(r rowScanner) (Proposal, error) {
	var (
		p                   Proposal
		msgID, appliedBy    *uuid.UUID
		appliedAt, undoneAt *time.Time
		change, applied     []byte
	)
	err := r.Scan(&p.ID, &p.OrgID, &p.ChatID, &msgID, &change, &p.Status, &applied, &appliedBy, &appliedAt, &undoneAt, &p.CreatedAt)
	if err != nil {
		return Proposal{}, err
	}
	if err := json.Unmarshal(change, &p.Change); err != nil {
		return Proposal{}, err
	}
	if applied != nil {
		if err := json.Unmarshal(applied, &p.Applied); err != nil {
			return Proposal{}, err
		}
	}
	if msgID != nil {
		p.MessageID = *msgID
	}
	if appliedBy != nil {
		p.AppliedBy = *appliedBy
	}
	if appliedAt != nil {
		p.AppliedAt = *appliedAt
	}
	if undoneAt != nil {
		p.UndoneAt = *undoneAt
	}
	return p, nil
}

func (s *PostgresStore) Proposal(ctx context.Context, orgID, id uuid.UUID) (Proposal, error) {
	p, err := scanProposal(s.pool.QueryRow(ctx,
		`SELECT `+proposalCols+` FROM assistant_proposals WHERE id = $1 AND org_id = $2`, id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	return p, err
}

func (s *PostgresStore) ChatProposals(ctx context.Context, orgID, chatID uuid.UUID) ([]Proposal, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+proposalCols+` FROM assistant_proposals
		 WHERE org_id = $1 AND chat_id = $2 ORDER BY created_at, id`, orgID, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProposal is one UPDATE with the status as a guard, so only one
// caller can move a proposal out of the status from.
func (s *PostgresStore) UpdateProposal(ctx context.Context, p Proposal, from string) error {
	change, err := json.Marshal(p.Change)
	if err != nil {
		return err
	}
	applied, err := jsonOrNull(p.Applied)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE assistant_proposals
		 SET change = $3, status = $4, applied = $5, applied_by = $6, applied_at = $7, undone_at = $8
		 WHERE id = $1 AND org_id = $2 AND status = $9`,
		p.ID, p.OrgID, change, p.Status, applied, nullUUID(p.AppliedBy), nullTime(p.AppliedAt), nullTime(p.UndoneAt), from)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var found bool
	err = s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM assistant_proposals WHERE id = $1 AND org_id = $2)`, p.ID, p.OrgID).Scan(&found)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	return ErrNotPending
}

// Reserve moves two counters in one transaction. Each upsert only
// counts up while the row is under its limit. The day row comes first,
// in all calls, so the row locks have one order.
func (s *PostgresStore) Reserve(ctx context.Context, r Reservation) (UsageView, error) {
	dk, mk := usageKeys(r.OrgID, r.UserID, r.At)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UsageView{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	day, ok, err := s.reserveRow(ctx, tx, dk, r.PerDay)
	if err != nil {
		return UsageView{}, err
	}
	if !ok {
		return UsageView{}, &LimitError{Scope: "day", ResetAt: dayReset(r.At), Used: day, Limit: r.PerDay}
	}
	month, ok, err := s.reserveRow(ctx, tx, mk, r.PerMonth)
	if err != nil {
		return UsageView{}, err
	}
	if !ok {
		return UsageView{}, &LimitError{Scope: "month", ResetAt: monthReset(r.At), Used: month, Limit: r.PerMonth}
	}
	if err := tx.Commit(ctx); err != nil {
		return UsageView{}, err
	}
	return usageView(r, day, month), nil
}

// reserveRow adds one question. It gives the count, and false if the
// limit stopped it (then the count is the stored one).
func (s *PostgresStore) reserveRow(ctx context.Context, tx pgx.Tx, k usageKey, limit int) (int, bool, error) {
	var n int
	err := tx.QueryRow(ctx,
		`INSERT INTO assistant_usage (org_id, scope, subject, period, questions)
		 VALUES ($1, $2, $3, $4, 1)
		 ON CONFLICT (org_id, scope, subject, period) DO UPDATE
		   SET questions = assistant_usage.questions + 1
		   WHERE $5::int <= 0 OR assistant_usage.questions < $5::int
		 RETURNING questions`,
		k.org, k.scope, k.subject, k.period, limit).Scan(&n)
	if err == nil {
		return n, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	err = tx.QueryRow(ctx,
		`SELECT questions FROM assistant_usage WHERE org_id = $1 AND scope = $2 AND subject = $3 AND period = $4`,
		k.org, k.scope, k.subject, k.period).Scan(&n)
	return n, false, err
}

func (s *PostgresStore) AddUsage(ctx context.Context, orgID, userID uuid.UUID, at time.Time, tokens, toolCalls int) error {
	dk, mk := usageKeys(orgID, userID, at)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, k := range []usageKey{dk, mk} {
		_, err := tx.Exec(ctx,
			`INSERT INTO assistant_usage (org_id, scope, subject, period, tokens, tool_calls)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (org_id, scope, subject, period) DO UPDATE
			   SET tokens = assistant_usage.tokens + EXCLUDED.tokens,
			       tool_calls = assistant_usage.tool_calls + EXCLUDED.tool_calls`,
			k.org, k.scope, k.subject, k.period, tokens, toolCalls)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Usage(ctx context.Context, orgID, userID uuid.UUID, at time.Time) (UsageView, error) {
	dk, mk := usageKeys(orgID, userID, at)
	var day, month int
	for k, dst := range map[usageKey]*int{dk: &day, mk: &month} {
		err := s.pool.QueryRow(ctx,
			`SELECT questions FROM assistant_usage WHERE org_id = $1 AND scope = $2 AND subject = $3 AND period = $4`,
			k.org, k.scope, k.subject, k.period).Scan(dst)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return UsageView{}, err
		}
	}
	return usageView(Reservation{At: at}, day, month), nil
}

func (s *PostgresStore) OrgsWithChats(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT org_id FROM assistant_chats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteChatsBefore deletes the chats. The messages and the proposals go
// with them (ON DELETE CASCADE).
func (s *PostgresStore) DeleteChatsBefore(ctx context.Context, orgID uuid.UUID, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM assistant_chats WHERE org_id = $1 AND updated_at < $2`, orgID, before)
	return int(tag.RowsAffected()), err
}
