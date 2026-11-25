package assistant

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/config"
	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/store"
)

// prefix is the part of a request that all orgs share.
func prefix(t *testing.T, s *Service, a Actor) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		System []string
		Tools  []ToolDef
	}{systemPrompt(s.cfg), s.newToolbox(a, store.AIChangesAlertsAndRules).defs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStablePrefix(t *testing.T) {
	svc := NewService(config.AssistantConfig{Model: "m"}, nil, &fakeTools{}, &fakeWriter{}, NewMemoryStore(), nil, nil, nil)
	a1 := Actor{OrgID: uuid.New(), UserID: uuid.New(), Role: store.RoleAdmin}
	a2 := Actor{OrgID: uuid.New(), UserID: uuid.New(), Role: store.RoleAdmin}
	p1, p2 := prefix(t, svc, a1), prefix(t, svc, a2)
	if string(p1) != string(p2) {
		t.Fatal("two orgs give different prefix bytes")
	}
	if re := regexp.MustCompile(`\d{4}-\d{2}-\d{2}`); re.Match(p1) {
		t.Fatalf("prefix has a date: %s", re.Find(p1))
	}
	for _, id := range []string{a1.OrgID.String(), a1.UserID.String()} {
		if strings.Contains(string(p1), id) {
			t.Fatal("prefix has an ID")
		}
	}
}

func TestUserContextHoldsVolatileData(t *testing.T) {
	q := Question{
		Text:     "why so many emails?",
		Context:  PageContext{Route: "/hosts/h_1", HostID: "h_1", Metric: "cpu", From: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)},
		Mentions: []string{"h_2"},
	}
	got := userContext("Acme", time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC), q)
	for _, want := range []string{"Organization: Acme", "Time now: 2026-11-25T10:00:00Z", "Page: /hosts/h_1", "Host: h_1", "Metric: cpu", "Range: 2026-11-01T00:00:00Z to 2026-11-25T00:00:00Z", "h_2", "Question:\nwhy so many emails?"} {
		if !strings.Contains(got, want) {
			t.Errorf("context lacks %q:\n%s", want, got)
		}
	}
}

func TestSystemPromptHasFactsAndExtra(t *testing.T) {
	got := systemPrompt(config.AssistantConfig{SystemExtra: " Be kind. "})
	if len(got) != 3 || !strings.Contains(got[1], mcp.Instructions()) || got[2] != "Be kind." {
		t.Fatalf("blocks %d", len(got))
	}
	if len(systemPrompt(config.AssistantConfig{})) != 2 {
		t.Fatal("extra block without text")
	}
}

func TestPrefixBigEnoughToCache(t *testing.T) {
	srv := mcp.New(nil, mcp.Options{})
	svc := NewService(config.AssistantConfig{Model: "m"}, nil, srv, &fakeWriter{}, NewMemoryStore(), nil, nil, nil)
	a := Actor{OrgID: uuid.New(), UserID: uuid.New(), Role: store.RoleAdmin}
	tb := svc.newToolbox(a, store.AIChangesAlertsAndRules)
	if len(tb.write) != 7 {
		t.Fatalf("write tools %d, want 7", len(tb.write))
	}
	// 4096 tokens is the highest minimum cacheable prefix of the models.
	if tokens := len(prefix(t, svc, a)) / 4; tokens < 4096 {
		t.Fatalf("prefix is about %d tokens, want at least 4096", tokens)
	}
}
