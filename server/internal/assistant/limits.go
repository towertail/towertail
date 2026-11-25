package assistant

import (
	"context"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/config"
)

func authctxWith(ctx context.Context, a Actor) context.Context {
	return authctx.WithRole(authctx.WithToken(ctx, a.Token), a.Role)
}

// orgLimits are the limits that apply to one org.
type orgLimits struct {
	plan  string
	seats int
	lim   config.AssistantPlanLimits
	model string
	cfg   config.AssistantConfig
}

func (s *Service) limitsFor(ctx context.Context, orgID uuid.UUID) (orgLimits, error) {
	l := orgLimits{cfg: s.cfg}
	if s.plans != nil {
		var err error
		if l.plan, l.seats, err = s.plans.AssistantPlan(ctx, orgID); err != nil {
			return orgLimits{}, err
		}
	}
	l.lim = s.cfg.PlanLimits(l.plan)
	l.model = l.lim.Model
	if l.model == "" {
		l.model = s.cfg.Model
	}
	return l, nil
}

// perMonth gives the org pool for a month. pooled is true when seats set it.
func (l orgLimits) perMonth() (limit int, pooled bool) {
	if l.lim.QuestionsPerSeatMonth > 0 {
		return l.lim.QuestionsPerSeatMonth * max(l.seats, 1), true
	}
	return l.lim.QuestionsPerMonth, false
}

// maxTokens is the reply size of one model request.
func (l orgLimits) maxTokens() int {
	if l.lim.ReplyTokens > 0 {
		return l.lim.ReplyTokens
	}
	return l.cfg.MaxTokensOrDefault()
}

// webSearch tells if the plan allows web search. The plan value wins.
func (l orgLimits) webSearch() bool {
	if l.cfg.Format() != "anthropic" {
		return false
	}
	if l.lim.WebSearch != nil {
		return *l.lim.WebSearch
	}
	return l.cfg.WebSearch
}

// Usage gives the use and the limits of the user.
func (s *Service) Usage(ctx context.Context, a Actor) (UsageView, error) {
	off, ai, err := s.orgSettings(ctx, a.OrgID)
	if err != nil {
		return UsageView{}, err
	}
	l, err := s.limitsFor(ctx, a.OrgID)
	if err != nil {
		return UsageView{}, err
	}
	v, err := s.store.Usage(ctx, a.OrgID, a.UserID, s.now())
	if err != nil {
		return UsageView{}, err
	}
	month, pooled := l.perMonth()
	v.Enabled, v.Off, v.AIChanges = true, off, ai
	v.Plan, v.Model, v.ModelName = l.plan, l.model, modelName(l.model)
	v.Day.Limit = l.lim.QuestionsPerDay
	v.Month.Limit, v.Month.Pooled = month, pooled
	return v, nil
}

// modelName turns "claude-sonnet-5-5" into "Sonnet 5.5". Other ids stay.
func modelName(id string) string {
	rest, ok := strings.CutPrefix(id, "claude-")
	if !ok {
		return id
	}
	parts := strings.Split(rest, "-")
	name := []rune(parts[0])
	if len(name) == 0 || !unicode.IsLetter(name[0]) {
		return id
	}
	name[0] = unicode.ToUpper(name[0])
	if len(parts) == 1 {
		return string(name)
	}
	for _, p := range parts[1:] {
		if strings.Trim(p, "0123456789") != "" {
			return id
		}
	}
	return string(name) + " " + strings.Join(parts[1:], ".")
}
