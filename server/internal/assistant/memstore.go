package assistant

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is a Store in memory. Tests use it.
type MemoryStore struct {
	mu        sync.Mutex
	chats     map[uuid.UUID]Chat
	msgs      map[uuid.UUID][]StoredMessage
	proposals map[uuid.UUID]Proposal
	usage     map[usageKey]*usageRow
}

type usageKey struct {
	org     uuid.UUID
	scope   string // "user" | "org"
	subject string
	period  string
}

type usageRow struct{ questions, tokens, toolCalls int }

// NewMemoryStore makes an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		chats:     map[uuid.UUID]Chat{},
		msgs:      map[uuid.UUID][]StoredMessage{},
		proposals: map[uuid.UUID]Proposal{},
		usage:     map[usageKey]*usageRow{},
	}
}

func (s *MemoryStore) CreateChat(_ context.Context, c Chat) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chats[c.ID] = c
	return nil
}

func (s *MemoryStore) Chats(_ context.Context, orgID, userID uuid.UUID, limit int) ([]Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Chat
	for _, c := range s.chats {
		if c.OrgID == orgID && c.UserID == userID {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Chat) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) own(orgID, userID, chatID uuid.UUID) (Chat, bool) {
	c, ok := s.chats[chatID]
	return c, ok && c.OrgID == orgID && c.UserID == userID
}

func (s *MemoryStore) Chat(_ context.Context, orgID, userID, chatID uuid.UUID) (Chat, []StoredMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.own(orgID, userID, chatID)
	if !ok {
		return Chat{}, nil, ErrNotFound
	}
	return c, slices.Clone(s.msgs[chatID]), nil
}

func (s *MemoryStore) DeleteChat(_ context.Context, orgID, userID, chatID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.own(orgID, userID, chatID); !ok {
		return ErrNotFound
	}
	s.deleteChat(chatID)
	return nil
}

func (s *MemoryStore) deleteChat(chatID uuid.UUID) {
	delete(s.chats, chatID)
	delete(s.msgs, chatID)
	for id, p := range s.proposals {
		if p.ChatID == chatID {
			delete(s.proposals, id)
		}
	}
}

func (s *MemoryStore) AppendMessage(_ context.Context, m StoredMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.chats[m.ChatID]
	if !ok {
		return ErrNotFound
	}
	m.Seq = len(s.msgs[m.ChatID]) + 1
	s.msgs[m.ChatID] = append(s.msgs[m.ChatID], m)
	c.UpdatedAt = cmp.Or(m.CreatedAt, time.Now())
	s.chats[m.ChatID] = c
	return nil
}

func (s *MemoryStore) UpdateMessage(_ context.Context, m StoredMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.msgs[m.ChatID]
	i := slices.IndexFunc(list, func(x StoredMessage) bool { return x.ID == m.ID })
	if i < 0 {
		return ErrNotFound
	}
	m.Seq = list[i].Seq
	list[i] = m
	return nil
}

func (s *MemoryStore) SaveProposal(_ context.Context, p Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proposals[p.ID] = p
	return nil
}

func (s *MemoryStore) Proposal(_ context.Context, orgID, id uuid.UUID) (Proposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.proposals[id]
	if !ok || p.OrgID != orgID {
		return Proposal{}, ErrNotFound
	}
	return p, nil
}

func (s *MemoryStore) UpdateProposal(_ context.Context, p Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.proposals[p.ID]; !ok {
		return ErrNotFound
	}
	s.proposals[p.ID] = p
	return nil
}

func dayPeriod(at time.Time) string   { return "d:" + at.UTC().Format("2006-01-02") }
func monthPeriod(at time.Time) string { return "m:" + at.UTC().Format("2006-01") }

func dayReset(at time.Time) time.Time {
	y, m, d := at.UTC().Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
}

func monthReset(at time.Time) time.Time {
	y, m, _ := at.UTC().Date()
	return time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
}

func (s *MemoryStore) row(k usageKey) *usageRow {
	r := s.usage[k]
	if r == nil {
		r = &usageRow{}
		s.usage[k] = r
	}
	return r
}

func (s *MemoryStore) keys(orgID, userID uuid.UUID, at time.Time) (day, month usageKey) {
	return usageKey{orgID, "user", userID.String(), dayPeriod(at)},
		usageKey{orgID, "org", "", monthPeriod(at)}
}

func (s *MemoryStore) view(r Reservation, day, month *usageRow) UsageView {
	return UsageView{
		Day:   WindowUsage{Used: day.questions, Limit: r.PerDay, ResetAt: dayReset(r.At)},
		Month: WindowUsage{Used: month.questions, Limit: r.PerMonth, ResetAt: monthReset(r.At)},
	}
}

func (s *MemoryStore) Reserve(_ context.Context, r Reservation) (UsageView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dk, mk := s.keys(r.OrgID, r.UserID, r.At)
	day, month := s.row(dk), s.row(mk)
	if r.PerDay > 0 && day.questions >= r.PerDay {
		return UsageView{}, &LimitError{Scope: "day", ResetAt: dayReset(r.At), Used: day.questions, Limit: r.PerDay}
	}
	if r.PerMonth > 0 && month.questions >= r.PerMonth {
		return UsageView{}, &LimitError{Scope: "month", ResetAt: monthReset(r.At), Used: month.questions, Limit: r.PerMonth}
	}
	day.questions++
	month.questions++
	return s.view(r, day, month), nil
}

func (s *MemoryStore) AddUsage(_ context.Context, orgID, userID uuid.UUID, at time.Time, tokens, toolCalls int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dk, mk := s.keys(orgID, userID, at)
	for _, k := range []usageKey{dk, mk} {
		r := s.row(k)
		r.tokens += tokens
		r.toolCalls += toolCalls
	}
	return nil
}

func (s *MemoryStore) Usage(_ context.Context, orgID, userID uuid.UUID, at time.Time) (UsageView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dk, mk := s.keys(orgID, userID, at)
	return s.view(Reservation{At: at}, s.row(dk), s.row(mk)), nil
}

func (s *MemoryStore) OrgsWithChats(_ context.Context) ([]uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, c := range s.chats {
		if !seen[c.OrgID] {
			seen[c.OrgID] = true
			out = append(out, c.OrgID)
		}
	}
	return out, nil
}

func (s *MemoryStore) DeleteChatsBefore(_ context.Context, orgID uuid.UUID, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, c := range s.chats {
		if c.OrgID == orgID && c.UpdatedAt.Before(before) {
			s.deleteChat(id)
			n++
		}
	}
	return n, nil
}
