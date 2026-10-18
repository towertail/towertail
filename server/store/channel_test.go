package store

import (
	"testing"

	"github.com/towertail/towertail/server/notify"
	"github.com/towertail/towertail/server/wire"
)

func TestChannelRouteMatch(t *testing.T) {
	alert := func(metric string, from, to wire.Tint, tags ...string) notify.Alert {
		return notify.Alert{Metric: metric, From: from, To: to, Tags: tags}
	}
	for _, tc := range []struct {
		name  string
		route ChannelRoute
		a     notify.Alert
		want  bool
	}{
		{"default takes warn", ChannelRoute{}, alert("cpu", wire.TintOK, wire.TintWarn), true},
		{"critical only skips warn", ChannelRoute{MinTint: wire.TintCritical}, alert("cpu", wire.TintOK, wire.TintWarn), false},
		{"critical only takes critical", ChannelRoute{MinTint: wire.TintCritical}, alert("cpu", wire.TintWarn, wire.TintCritical), true},
		{"ok after critical", ChannelRoute{MinTint: wire.TintCritical}, alert("cpu", wire.TintCritical, wire.TintOK), true},
		{"ok after warn", ChannelRoute{MinTint: wire.TintCritical}, alert("cpu", wire.TintWarn, wire.TintOK), false},
		{"metric filter", ChannelRoute{Metrics: []string{"disk"}}, alert("cpu", wire.TintOK, wire.TintWarn), false},
		{"metric in filter", ChannelRoute{Metrics: []string{"disk", "cpu"}}, alert("cpu", wire.TintOK, wire.TintWarn), true},
		{"tag filter, no tags", ChannelRoute{Tags: []string{"prod"}}, alert("cpu", wire.TintOK, wire.TintWarn), false},
		{"tag filter, one tag", ChannelRoute{Tags: []string{"prod"}}, alert("cpu", wire.TintOK, wire.TintWarn, "db", "prod"), true},
	} {
		if got := tc.route.Match(tc.a); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}
