package wire

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AICall is one AI tool call. It has no result values. Input is cut to 200
// characters in a list.
type AICall struct {
	ID         uuid.UUID  `json:"id"`
	TS         time.Time  `json:"ts"`
	UserID     *uuid.UUID `json:"userId,omitempty"`
	Via        string     `json:"via"` // api_key, oauth, user or assistant
	Client     string     `json:"client"`
	GrantID    *uuid.UUID `json:"grantId,omitempty"`
	TokenID    uuid.UUID  `json:"tokenId"`
	Tool       string     `json:"tool"`
	Input      string     `json:"input"`
	Status     string     `json:"status"` // ok, error, denied or rate_limited
	Clamped    bool       `json:"clamped"`
	Error      string     `json:"error,omitempty"`
	DurationMs int        `json:"durationMs"`
	Rows       int        `json:"rows"`
	Bytes      int        `json:"bytes"`
}

// AICallDetail is one call with the full input and, for a write tool, the
// change (targets, field changes and backup_id).
type AICallDetail struct {
	AICall
	Change json.RawMessage `json:"change,omitempty"`
}

// AICallPage is one page of calls, newest first. Next is the cursor of the
// next page. It is empty on the last page.
type AICallPage struct {
	Calls []AICall `json:"calls"`
	Next  string   `json:"next,omitempty"`
}

// AICallStats are the totals of a range. Clients and Tools are the distinct
// names of the range.
type AICallStats struct {
	Calls   int      `json:"calls"`
	People  int      `json:"people"`
	Errors  int      `json:"errors"`
	Clamped int      `json:"clamped"`
	P95Ms   int      `json:"p95Ms"`
	Clients []string `json:"clients"`
	Tools   []string `json:"tools"`
}

// AIUsage is the use of one connected assistant (a grant), or of one token
// that has no grant.
type AIUsage struct {
	GrantID *uuid.UUID `json:"grantId,omitempty"`
	TokenID *uuid.UUID `json:"tokenId,omitempty"`
	Calls   int        `json:"calls"`
	LastAt  time.Time  `json:"lastAt"`
}

// AITool is one tool of the MCP server.
type AITool struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Group       string `json:"group"`
	ReadOnly    bool   `json:"readOnly"`
	Destructive bool   `json:"destructive"`
}
