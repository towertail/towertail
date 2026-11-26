package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

var (
	// ErrNotFound means that a chat or a proposal does not exist for this
	// org and user.
	ErrNotFound = errors.New("assistant: not found")
	// ErrLimit is the errors.Is target of *LimitError.
	ErrLimit = errors.New("assistant: limit reached")
)

// Chat is a conversation of one user.
type Chat struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"-"`
	UserID    uuid.UUID `json:"-"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Message statuses.
const (
	StatusDone    = "done"
	StatusStopped = "stopped"
	StatusError   = "error"
)

// StoredMessage is one message of a chat. Native is the history for the
// model. Parts are the events that the web shows. A user message that only
// holds tool results has no Parts.
type StoredMessage struct {
	ID        uuid.UUID `json:"id"`
	ChatID    uuid.UUID `json:"chatId"`
	Seq       int       `json:"seq"`
	Role      string    `json:"role"`
	Native    []Part    `json:"native,omitempty"`
	Parts     []Event   `json:"parts,omitempty"`
	Model     string    `json:"model,omitempty"`
	Usage     *Usage    `json:"usage,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// ChatStore keeps chats and messages. Each read is scoped to the org and
// the user. Chat gives the messages in order of Seq. AppendMessage gives
// the message the next Seq and updates the chat.
type ChatStore interface {
	CreateChat(ctx context.Context, c Chat) error
	Chats(ctx context.Context, orgID, userID uuid.UUID, limit int) ([]Chat, error)
	Chat(ctx context.Context, orgID, userID, chatID uuid.UUID) (Chat, []StoredMessage, error)
	DeleteChat(ctx context.Context, orgID, userID, chatID uuid.UUID) error
	AppendMessage(ctx context.Context, m StoredMessage) error
	UpdateMessage(ctx context.Context, m StoredMessage) error
}

// ProposalStore keeps proposals.
type ProposalStore interface {
	SaveProposal(ctx context.Context, p Proposal) error
	Proposal(ctx context.Context, orgID, id uuid.UUID) (Proposal, error)
	// ChatProposals gives the proposals of one chat, oldest first.
	ChatProposals(ctx context.Context, orgID, chatID uuid.UUID) ([]Proposal, error)
	// UpdateProposal saves p only if the stored status is from. Else it
	// gives ErrNotPending. This is a compare-and-set, so two servers
	// cannot take one proposal.
	UpdateProposal(ctx context.Context, p Proposal, from string) error
}

// UsageStore counts questions, tokens and tool calls.
type UsageStore interface {
	// Reserve adds one question to the user day and to the org month, in
	// one atomic step, or gives *LimitError. The view has the counts after
	// the step.
	Reserve(ctx context.Context, r Reservation) (UsageView, error)
	AddUsage(ctx context.Context, orgID, userID uuid.UUID, at time.Time, tokens, toolCalls int) error
	Usage(ctx context.Context, orgID, userID uuid.UUID, at time.Time) (UsageView, error)
}

// RetentionStore deletes old chats.
type RetentionStore interface {
	OrgsWithChats(ctx context.Context) ([]uuid.UUID, error)
	DeleteChatsBefore(ctx context.Context, orgID uuid.UUID, before time.Time) (int, error)
}

// Store is everything that the assistant keeps.
type Store interface {
	ChatStore
	ProposalStore
	UsageStore
	RetentionStore
}

// Reservation asks for one question. A zero limit means no limit.
type Reservation struct {
	OrgID, UserID uuid.UUID
	At            time.Time // UTC
	PerDay        int
	PerMonth      int // the org pool
}

// LimitError tells which limit stopped a question. Scope is "day" or
// "month".
type LimitError struct {
	Scope   string
	ResetAt time.Time
	Used    int
	Limit   int
}

func (e *LimitError) Error() string   { return "assistant: " + e.Scope + " limit reached" }
func (e *LimitError) Is(t error) bool { return t == ErrLimit }

// UsageView is the use and the limits of a user, for the usage route.
// The store fills the counts. The service fills the other fields.
type UsageView struct {
	Enabled   bool            `json:"enabled"`
	Off       bool            `json:"off"`
	Plan      string          `json:"plan,omitempty"`
	Model     string          `json:"model,omitempty"`
	ModelName string          `json:"modelName,omitempty"`
	Day       WindowUsage     `json:"day"`
	Month     WindowUsage     `json:"month"`
	AIChanges store.AIChanges `json:"aiChanges,omitempty"`
}
