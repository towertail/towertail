package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/towertail/towertail/server/internal/mcp"
)

// Component kinds.
const (
	KindBarChart  = "bar_chart"
	KindLineChart = "line_chart"
	KindCitation  = "citation"
)

const (
	maxBars       = 50
	maxThresholds = 4
	maxCiteRows   = 200
	defaultRange  = "7d"
)

// Component is a chart or a citation in the answer.
type Component struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"`
	Title      string      `json:"title,omitempty"`
	Unit       string      `json:"unit,omitempty"`
	Source     string      `json:"source,omitempty"`
	Bars       []Bar       `json:"bars,omitempty"`
	Series     []Series    `json:"series,omitempty"`
	Thresholds []Threshold `json:"thresholds,omitempty"`
	Label      string      `json:"label,omitempty"`
	Rows       []string    `json:"rows,omitempty"`
	Href       string      `json:"href,omitempty"`
}

// Bar is one bar of a bar chart.
type Bar struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// Series is one line of a line chart. A point is [unix seconds, value].
type Series struct {
	Name   string       `json:"name"`
	Points [][2]float64 `json:"points"`
}

// Threshold is a horizontal line of a chart.
type Threshold struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Style string  `json:"style"` // "current" | "proposed"
}

type showChartInput struct {
	Kind       string      `json:"kind"`
	Title      string      `json:"title"`
	Unit       string      `json:"unit"`
	Source     string      `json:"source"`
	Host       string      `json:"host"`
	Metric     string      `json:"metric"`
	From       string      `json:"from"`
	To         string      `json:"to"`
	Bars       []Bar       `json:"bars"`
	Thresholds []Threshold `json:"thresholds"`
}

type citeInput struct {
	Label  string   `json:"label"`
	Source string   `json:"source"`
	Rows   []string `json:"rows"`
	Href   string   `json:"href"`
}

// chart builds a chart component from the input of show_chart. known maps
// the read tool calls of this question.
func (t *turn) chart(ctx context.Context, raw json.RawMessage) (Component, error) {
	var in showChartInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Component{}, fmt.Errorf("bad input: %w", err)
	}
	c := Component{Kind: in.Kind, Title: in.Title, Unit: in.Unit, Source: in.Source}
	if len(in.Thresholds) > maxThresholds {
		return Component{}, fmt.Errorf("at most %d thresholds", maxThresholds)
	}
	for _, th := range in.Thresholds {
		if th.Label == "" || (th.Style != "current" && th.Style != "proposed") {
			return Component{}, errors.New("a threshold needs a label and a style of current or proposed")
		}
	}
	c.Thresholds = in.Thresholds
	switch in.Kind {
	case KindBarChart:
		if !t.known(in.Source) {
			return Component{}, errors.New("source must be the ID of a read tool call of this question")
		}
		if len(in.Bars) == 0 || len(in.Bars) > maxBars {
			return Component{}, fmt.Errorf("bars must have 1 to %d items", maxBars)
		}
		c.Bars = in.Bars
	case KindLineChart:
		if in.Host == "" || in.Metric == "" {
			return Component{}, errors.New("a line chart needs host and metric")
		}
		s, err := t.series(ctx, in)
		if err != nil {
			return Component{}, err
		}
		c.Series = []Series{s.Series}
		if c.Unit == "" {
			c.Unit = s.Unit
		}
	default:
		return Component{}, errors.New("kind must be bar_chart or line_chart")
	}
	return c, nil
}

type seriesResult struct {
	Series
	Unit string
}

// series reads the points of a line chart from host_metrics.
func (t *turn) series(ctx context.Context, in showChartInput) (seriesResult, error) {
	args := map[string]any{"host": in.Host, "metrics": []string{in.Metric}}
	if in.From != "" {
		args["from"] = in.From
		if in.To != "" {
			args["to"] = in.To
		}
	} else {
		args["range"] = defaultRange
	}
	b, _ := json.Marshal(args)
	out, err := t.s.tools.Call(t.a.ctx(ctx), "host_metrics", b)
	if err != nil {
		return seriesResult{}, fmt.Errorf("host_metrics: %w", err)
	}
	var res mcp.HostMetricsOut
	if err := json.Unmarshal(out, &res); err != nil {
		return seriesResult{}, err
	}
	for _, h := range res.Hosts {
		for _, m := range h.Metrics {
			if m.Count == 0 || len(m.Points) == 0 {
				continue
			}
			s := seriesResult{Series: Series{Name: h.Host + " " + m.Metric}, Unit: m.Unit}
			for _, p := range m.Points {
				ts, err := time.Parse(time.RFC3339, p.TS)
				if err != nil {
					return seriesResult{}, fmt.Errorf("bad point time: %w", err)
				}
				s.Points = append(s.Points, [2]float64{float64(ts.Unix()), p.Value})
			}
			return s, nil
		}
	}
	return seriesResult{}, errors.New("no data for this host, metric and range")
}

// citation builds a citation component from the input of cite.
func (t *turn) citation(raw json.RawMessage) (Component, error) {
	var in citeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Component{}, fmt.Errorf("bad input: %w", err)
	}
	switch {
	case in.Label == "":
		return Component{}, errors.New("label is required")
	case len(in.Rows) == 0 || len(in.Rows) > maxCiteRows:
		return Component{}, fmt.Errorf("rows must have 1 to %d items", maxCiteRows)
	case !t.known(in.Source):
		return Component{}, errors.New("source must be the ID of a read tool call of this question")
	case in.Href != "" && (!strings.HasPrefix(in.Href, "/") || strings.HasPrefix(in.Href, "//")):
		return Component{}, errors.New("href must be a path in this app, for example /hosts/h_1")
	}
	return Component{Kind: KindCitation, Label: in.Label, Source: in.Source, Rows: in.Rows, Href: in.Href}, nil
}

func (t *turn) known(source string) bool {
	_, ok := t.calls[source]
	return ok
}
