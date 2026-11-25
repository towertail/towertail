package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Limits of the tool replies.
const (
	defaultHosts = 50
	maxHosts     = 200
)

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return ts(*t)
}

// hostID gives the node ID of a host reference. An empty reference means
// the whole fleet and gives uuid.Nil.
func (s *Server) hostID(ctx context.Context, orgID uuid.UUID, ref string) (uuid.UUID, error) {
	if strings.TrimSpace(ref) == "" {
		return uuid.Nil, nil
	}
	n, err := s.q.ResolveHost(ctx, orgID, ref)
	return n.ID, err
}
