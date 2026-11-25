package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/internal/query"
)

// Limits of search. Each source has its own share, so a result list is
// never all hosts.
const (
	maxSearchHosts = 10
	maxSearchAlert = 5
	maxSearchRefs  = 5
)

// Kinds of the ids of search and fetch.
const (
	kindHost      = "host"
	kindAlert     = "alert"
	kindReference = "reference"
)

// SearchIn is the input of search.
type SearchIn struct {
	Query string `json:"query" jsonschema:"text to find in host names and tags, in the recent alerts (host, metric, process) and in the reference topics"`
}

// SearchResult is one hit. ID is host:<uuid>, alert:<uuid> or
// reference:<topic>. URL is the dashboard page, when the server knows it.
type SearchResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

// SearchOut is the output of search. It has at most 20 results: hosts,
// then alerts of the last 7 days, then reference topics.
type SearchOut struct {
	Results []SearchResult `json:"results"`
}

// FetchIn is the input of fetch.
type FetchIn struct {
	ID string `json:"id" jsonschema:"an id from search: host:<uuid>, alert:<uuid> or reference:<topic>"`
}

// FetchMetadata describes the document of fetch. Kind is host, alert or
// reference.
type FetchMetadata struct {
	Kind string `json:"kind"`
}

// FetchOut is the output of fetch. Text is the JSON that get_host,
// get_alert or reference gives for the id.
type FetchOut struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Text     string        `json:"text"`
	URL      string        `json:"url,omitempty"`
	Metadata FetchMetadata `json:"metadata"`
}

// url gives the dashboard page of a result, or empty when the server has no
// app URL or the result has no page.
func (s *Server) url(kind, id string) string {
	base := strings.TrimRight(s.appURL, "/")
	if base == "" {
		return ""
	}
	switch kind {
	case kindHost:
		return base + "/hosts/" + id
	case kindAlert:
		return base + "/alerts"
	}
	return ""
}

func (s *Server) search(ctx context.Context, _ *sdk.CallToolRequest, in SearchIn) (*sdk.CallToolResult, SearchOut, error) {
	c, err := callerOf(ctx)
	if err != nil {
		return nil, SearchOut{}, err
	}
	orgID := c.Token.OrgID
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, SearchOut{}, badf("query is required")
	}
	out := SearchOut{Results: []SearchResult{}}
	hosts, err := s.searchHosts(ctx, orgID, q)
	if err != nil {
		return nil, SearchOut{}, s.toolError("search", err)
	}
	out.Results = append(out.Results, hosts...)
	now := s.now().UTC()
	page, err := s.q.ListAlerts(ctx, query.AlertQuery{
		OrgID: orgID, State: "all", Q: q, From: now.Add(-7 * 24 * time.Hour), To: now, Sort: "start", Desc: true, Limit: maxSearchAlert, Now: now,
	})
	switch {
	case errors.Is(err, query.ErrNoHistory):
	case err != nil:
		return nil, SearchOut{}, s.toolError("search", err)
	}
	for _, a := range page.Alerts {
		item := newAlertItem(a, "", now)
		out.Results = append(out.Results, SearchResult{ID: kindAlert + ":" + item.ID, Title: alertTitle(item), URL: s.url(kindAlert, item.ID)})
	}
	refs, err := s.searchReference(ctx, orgID, q)
	if err != nil {
		return nil, SearchOut{}, s.toolError("search", err)
	}
	out.Results = append(out.Results, refs...)
	return nil, out, nil
}

// searchHosts finds the hosts whose name contains the text (no case) and
// the hosts that have the text as a tag.
func (s *Server) searchHosts(ctx context.Context, orgID uuid.UUID, q string) ([]SearchResult, error) {
	names, err := s.q.HostNames(ctx, orgID)
	if err != nil {
		return nil, err
	}
	found := map[uuid.UUID]string{}
	for id, name := range names {
		if strings.Contains(strings.ToLower(name), strings.ToLower(q)) {
			found[id] = name
		}
	}
	tagged, err := s.q.HostsByTag(ctx, orgID, q)
	if err != nil {
		return nil, err
	}
	for _, n := range tagged {
		found[n.ID] = n.DisplayName
	}
	ids := make([]uuid.UUID, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int {
		return cmp.Or(strings.Compare(found[a], found[b]), strings.Compare(a.String(), b.String()))
	})
	out := make([]SearchResult, 0, min(len(ids), maxSearchHosts))
	for _, id := range ids[:min(len(ids), maxSearchHosts)] {
		out = append(out, SearchResult{ID: kindHost + ":" + id.String(), Title: found[id], URL: s.url(kindHost, id.String())})
	}
	return out, nil
}

// searchReference finds the topics whose name or facts contain the text.
// A space or a dash in the text matches the underscore of a topic name.
func (s *Server) searchReference(ctx context.Context, orgID uuid.UUID, q string) ([]SearchResult, error) {
	facts, err := s.orgFacts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(q)
	name := strings.NewReplacer(" ", "_", "-", "_").Replace(q)
	var out []SearchResult
	for _, topic := range referenceTopics {
		ref, err := s.ref.Topic(topic, facts)
		if err != nil {
			return nil, err
		}
		hit := strings.Contains(topic, name) || slices.ContainsFunc(ref.Facts, func(f ReferenceFact) bool {
			return strings.Contains(strings.ToLower(f.Name), q) || strings.Contains(strings.ToLower(f.Text), q)
		})
		if hit && len(out) < maxSearchRefs {
			out = append(out, SearchResult{ID: kindReference + ":" + topic, Title: "Reference: " + topic})
		}
	}
	return out, nil
}

// alertTitle names an alert in a result list.
func alertTitle(a AlertItem) string {
	return fmt.Sprintf("%s %s %s alert (%s), %s", a.Host, a.Metric, a.Severity, a.State, a.Start)
}

func (s *Server) fetch(ctx context.Context, req *sdk.CallToolRequest, in FetchIn) (*sdk.CallToolResult, FetchOut, error) {
	kind, ref, _ := strings.Cut(strings.TrimSpace(in.ID), ":")
	out := FetchOut{ID: kind + ":" + ref, Metadata: FetchMetadata{Kind: kind}}
	var doc any
	switch kind {
	case kindHost, kindAlert:
		if _, err := uuid.Parse(ref); err != nil {
			return nil, FetchOut{}, badf("id must be %s:<uuid>, from search", kind)
		}
		out.URL = s.url(kind, ref)
		if kind == kindHost {
			_, h, err := s.getHost(ctx, req, GetHostIn{Host: ref})
			if err != nil {
				return nil, FetchOut{}, err
			}
			out.Title, doc = h.Name, h
			break
		}
		_, a, err := s.getAlert(ctx, req, GetAlertIn{AlertID: ref})
		if err != nil {
			return nil, FetchOut{}, err
		}
		out.Title, doc = alertTitle(a.Alert), a
	case kindReference:
		_, r, err := s.reference(ctx, req, ReferenceIn{Topic: ref})
		if err != nil {
			return nil, FetchOut{}, err
		}
		out.Title, doc = "Reference: "+ref, r
	default:
		return nil, FetchOut{}, badf("id must start with host:, alert: or reference:, as in the results of search")
	}
	text, err := json.Marshal(doc)
	if err != nil {
		return nil, FetchOut{}, s.toolError("fetch", err)
	}
	out.Text = string(text)
	return nil, out, nil
}
