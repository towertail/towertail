package assistant

import (
	"slices"
	"strings"

	"github.com/towertail/towertail/server/internal/mcp"
	"github.com/towertail/towertail/server/store"
)

// Names of the tools that only the embedded assistant has.
const (
	toolShowChart = "show_chart"
	toolCite      = "cite"
)

// writeKinds gives the lowest kind and role of each write tool. A
// mute_metric on the org needs more. Plan tells the real kind.
var writeKinds = map[string]struct {
	kind string
	role store.Role
}{
	"ack_alert":         {"alerts", store.RoleMember},
	"snooze_alert":      {"alerts", store.RoleMember},
	"snooze_host":       {"alerts", store.RoleMember},
	"mute_metric":       {"alerts", store.RoleMember},
	"set_thresholds":    {"rules", store.RoleAdmin},
	"ignore_process":    {"rules", store.RoleAdmin},
	"undo_rules_change": {"rules", store.RoleAdmin},
}

const showChartSchema = `{"type":"object","properties":{
"kind":{"type":"string","enum":["bar_chart","line_chart"]},
"title":{"type":"string","description":"Short title with the range, for example \"Emails by rule, 30 d\"."},
"unit":{"type":"string","description":"Unit of the values, for example emails or %."},
"source":{"type":"string","description":"bar_chart: the ID of the tool call that gave the values."},
"host":{"type":"string","description":"line_chart: host ID or name."},
"metric":{"type":"string","description":"line_chart: one history metric, for example cpu."},
"from":{"type":"string","description":"line_chart: start in RFC 3339. Default is the last 7 days."},
"to":{"type":"string","description":"line_chart: end in RFC 3339. Default is now."},
"bars":{"type":"array","maxItems":50,"items":{"type":"object","properties":{"label":{"type":"string"},"value":{"type":"number"}},"required":["label","value"]}},
"thresholds":{"type":"array","maxItems":4,"items":{"type":"object","properties":{"label":{"type":"string"},"value":{"type":"number"},"style":{"type":"string","enum":["current","proposed"]}},"required":["label","value","style"]}}
},"required":["kind","title"]}`

const citeSchema = `{"type":"object","properties":{
"label":{"type":"string","description":"Short text of the source, for example \"processes 02:10\"."},
"source":{"type":"string","description":"The ID of the tool call that gave the rows."},
"rows":{"type":"array","maxItems":200,"items":{"type":"string"},"description":"IDs of the rows that prove the statement."},
"href":{"type":"string","description":"Optional path in this app that shows the rows, for example /hosts/h_1?tab=processes."}
},"required":["label","source","rows"]}`

// toolbox is the tool list of one question.
type toolbox struct {
	defs  []ToolDef // sorted by name
	read  map[string]mcp.ToolSpec
	write map[string]bool
}

// newToolbox lists the tools that the caller can use: the read tools, the
// write tools that the role and the org rule allow, show_chart and cite.
func (s *Service) newToolbox(a Actor, ai store.AIChanges) *toolbox {
	tb := &toolbox{read: map[string]mcp.ToolSpec{}, write: map[string]bool{}}
	for _, sp := range s.tools.ToolSpecs() {
		if sp.ReadOnly {
			tb.read[sp.Name] = sp
		} else if wk, ok := writeKinds[sp.Name]; ok && s.writer != nil && a.Role.Allows(wk.role) && permits(ai, wk.kind) {
			tb.write[sp.Name] = true
		} else {
			continue
		}
		tb.defs = append(tb.defs, ToolDef{Name: sp.Name, Description: sp.Description, Schema: sp.InputSchema})
	}
	tb.defs = append(tb.defs,
		ToolDef{Name: toolShowChart, Schema: []byte(showChartSchema), Description: "Show a chart in the answer. " +
			"For bar_chart, send the bars that you read and the source. For line_chart, send host and metric. The server reads the points."},
		ToolDef{Name: toolCite, Schema: []byte(citeSchema), Description: "Point at the rows of a tool result that prove a statement. The user sees the rows marked."},
	)
	slices.SortFunc(tb.defs, func(x, y ToolDef) int { return strings.Compare(x.Name, y.Name) })
	return tb
}

func (tb *toolbox) component(name string) bool { return name == toolShowChart || name == toolCite }
