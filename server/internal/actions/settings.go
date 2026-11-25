package actions

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/towertail/towertail/server/store"
)

// UpdateSettings decodes the body onto the stored settings, so a key that
// the body leaves out keeps its stored value. When the body changes a rule,
// the service sets the origin of the rules and makes a backup. The client
// and the device of the origin come from the rulesOrigin key of the body.
// The RulesResult is nil when no backup was made.
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
	by := Actor{OrgID: a.OrgID, By: a.By}
	if in.RulesOrigin != nil {
		by.Client, by.Device = in.RulesOrigin.Client, in.RulesOrigin.Device
	}
	res, err := s.EditRules(ctx, by, RulesEdit{
		Org: true,
		Apply: func(cfg *store.ServerSettings, _ map[uuid.UUID]*store.Node) error {
			if err := json.Unmarshal(body, cfg); err != nil {
				return badInput(err)
			}
			if err := cfg.Rules().Validate(); err != nil {
				return badInput(err)
			}
			if err := cfg.MCP.Validate(); err != nil {
				return badInput(err)
			}
			return nil
		},
	})
	if err != nil {
		return store.ServerSettings{}, nil, err
	}
	cfg := res.Settings
	if cfg == nil {
		c, err := s.st.GetSettings(ctx, a.OrgID)
		if err != nil {
			return store.ServerSettings{}, nil, err
		}
		cfg = &c
	}
	if res.Backup.ID == uuid.Nil {
		return *cfg, nil, nil
	}
	return *cfg, &res, nil
}
