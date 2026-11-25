package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/internal/actions"
	"github.com/towertail/towertail/server/store"
)

// hostRulesView is the rules of one host with the effective values.
type hostRulesView struct {
	NodeID    uuid.UUID            `json:"nodeId"`
	Host      store.HostRules      `json:"host"`
	Effective store.EffectiveRules `json:"effective"`
}

type rulesView struct {
	Org   store.OrgRules  `json:"org"`
	Hosts []hostRulesView `json:"hosts"`
}

type nodeRulesView struct {
	Org       store.OrgRules       `json:"org"`
	NodeID    uuid.UUID            `json:"nodeId"`
	Host      store.HostRules      `json:"host"`
	Effective store.EffectiveRules `json:"effective"`
}

func (s *Server) handleGetRules(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	ctx := c.Request().Context()
	cfg, err := s.store.GetSettings(ctx, t.OrgID)
	if err != nil {
		return err
	}
	nodes, err := s.store.ListNodes(ctx, t.OrgID)
	if err != nil {
		return err
	}
	org := cfg.Rules()
	out := rulesView{Org: org, Hosts: make([]hostRulesView, 0, len(nodes))}
	for _, n := range nodes {
		h := n.Rules()
		out.Hosts = append(out.Hosts, hostRulesView{NodeID: n.ID, Host: h, Effective: org.For(h)})
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) handleGetNodeRules(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	ctx := c.Request().Context()
	n, err := s.store.GetNode(ctx, t.OrgID, id)
	if err != nil {
		return err
	}
	cfg, err := s.store.GetSettings(ctx, t.OrgID)
	if err != nil {
		return err
	}
	org, h := cfg.Rules(), n.Rules()
	return c.JSON(http.StatusOK, nodeRulesView{Org: org, NodeID: n.ID, Host: h, Effective: org.For(h)})
}

// handlePutNodeRules replaces the rule fields of one host. A field that
// the body leaves out or sets to null clears that override.
func (s *Server) handlePutNodeRules(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	var h store.HostRules
	if err := c.Bind(&h); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	var client, device string
	if h.Origin != nil {
		client, device = h.Origin.Client, h.Origin.Device
	}
	n, _, err := s.actions.ReplaceHostRules(c.Request().Context(), actions.NewActor(t, client, device), id, h)
	if err != nil {
		return actionError(err)
	}
	s.audit(c, "update_rules", "node", n.ID, "client", store.NormalizeClient(client))
	return c.JSON(http.StatusOK, n)
}

type importHost struct {
	NodeID uuid.UUID       `json:"nodeId"`
	Rules  store.HostRules `json:"rules"`
}

type importBody struct {
	Org    *store.OrgRules `json:"org,omitempty"`
	Hosts  []importHost    `json:"hosts"`
	Device string          `json:"device"`
	Client string          `json:"client"`
}

type importResult struct {
	BackupID uuid.UUID `json:"backupId"`
	Hosts    int       `json:"hosts"`
	Org      bool      `json:"org"`
}

// handleImportRules replaces the org rules and the rules of the named
// hosts at once. It writes nothing when one part is bad.
func (s *Server) handleImportRules(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	var b importBody
	if err := c.Bind(&b); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if b.Org == nil && len(b.Hosts) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "nothing to import")
	}
	hosts := make(map[uuid.UUID]store.HostRules, len(b.Hosts))
	for _, h := range b.Hosts {
		if _, dup := hosts[h.NodeID]; dup {
			return echo.NewHTTPError(http.StatusBadRequest, "duplicate nodeId "+h.NodeID.String())
		}
		hosts[h.NodeID] = h.Rules
	}
	res, err := s.actions.Replace(c.Request().Context(), actions.NewActor(t, b.Client, b.Device), store.RulesChange{Org: b.Org, Hosts: hosts})
	if err != nil {
		return actionError(err)
	}
	s.audit(c, "import_rules", "rules", res.Backup.ID, "client", store.NormalizeClient(b.Client), "hosts", len(hosts), "org", b.Org != nil)
	return c.JSON(http.StatusOK, importResult{BackupID: res.Backup.ID, Hosts: len(hosts), Org: b.Org != nil})
}

func (s *Server) handleListRulesBackups(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	list, err := s.store.ListRulesBackups(c.Request().Context(), t.OrgID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []store.RulesBackup{}
	}
	return c.JSON(http.StatusOK, list)
}

// handleRestoreRulesBackup writes the old rules of a backup. The restore
// makes a new backup of the rules that it replaces. A host that no longer
// exists is skipped.
func (s *Server) handleRestoreRulesBackup(c echo.Context) error {
	t, _ := authctx.TokenFrom(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad id")
	}
	var b struct {
		Device string `json:"device"`
		Client string `json:"client"`
	}
	if err := c.Bind(&b); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	res, err := s.actions.Restore(c.Request().Context(), actions.NewActor(t, b.Client, b.Device), id, actions.RestoreAll)
	if err != nil {
		return actionError(err)
	}
	s.audit(c, "restore_rules", "rules", res.Backup.ID, "client", store.NormalizeClient(b.Client), "hosts", len(res.Nodes), "org", res.Settings != nil)
	return c.JSON(http.StatusOK, importResult{BackupID: res.Backup.ID, Hosts: len(res.Nodes), Org: res.Settings != nil})
}
