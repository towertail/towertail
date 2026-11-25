package actions

import (
	"context"
	"encoding/json"

	"github.com/towertail/towertail/server/store"
)

// UpdateSettings decodes the body onto the stored settings, so a key that
// the body leaves out keeps its stored value. When the body changes a rule,
// the service sets the origin of the rules. The client and the device of
// the origin come from the rulesOrigin key of the body.
func (s *Service) UpdateSettings(ctx context.Context, a Actor, body []byte) (store.ServerSettings, *RulesResult, error) {
	var in struct {
		RulesOrigin *struct {
			Client string `json:"client"`
			Device string `json:"device"`
		} `json:"rulesOrigin"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return store.ServerSettings{}, nil, badInput(err)
	}
	cfg, err := s.st.EditSettings(ctx, a.OrgID, func(cfg *store.ServerSettings) error {
		var oldOrigin *store.RulesOrigin // a copy: the decode writes through the pointer
		if cfg.RulesOrigin != nil {
			o := *cfg.RulesOrigin
			oldOrigin = &o
		}
		before := ruleKey(*cfg)
		if err := json.Unmarshal(body, cfg); err != nil {
			return badInput(err)
		}
		if err := cfg.Rules().Validate(); err != nil {
			return badInput(err)
		}
		if err := cfg.MCP.Validate(); err != nil {
			return badInput(err)
		}
		cfg.RulesOrigin = oldOrigin
		if ruleKey(*cfg) != before {
			by := Actor{By: a.By}
			if in.RulesOrigin != nil {
				by.Client, by.Device = in.RulesOrigin.Client, in.RulesOrigin.Device
			}
			o := by.Origin(s.now())
			cfg.RulesOrigin = &o
		}
		return nil
	})
	if err != nil {
		return store.ServerSettings{}, nil, err
	}
	s.invalidateSettings(a.OrgID)
	s.broadcastSettings(a.OrgID, cfg)
	return cfg, nil, nil
}

// ruleKey is the JSON of the org rules without the origin. Two equal keys
// mean that the rules did not change.
func ruleKey(cfg store.ServerSettings) string {
	r := cfg.Rules()
	r.Origin = nil
	b, _ := json.Marshal(r)
	return string(b)
}
