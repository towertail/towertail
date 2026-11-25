package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/towertail/towertail/server/authctx"
	"github.com/towertail/towertail/server/store"
)

func callerCtx() context.Context {
	return authctx.WithRole(authctx.WithToken(context.Background(), *tokenA()), store.RoleViewer)
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	var x, y any
	if err := remarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := remarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

func TestToolSpecs(t *testing.T) {
	s := newServer(newFake())
	specs := s.ToolSpecs()
	tools := s.Tools(ToolSetLatest)
	if len(specs) != len(tools) || len(specs) != 25 {
		t.Fatalf("specs = %d, tools = %d, want 25", len(specs), len(tools))
	}
	if !slices.IsSortedFunc(specs, func(a, b ToolSpec) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("specs are not sorted by name")
	}
	byName := map[string]*sdk.Tool{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	reads := 0
	for _, sp := range specs {
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(sp.InputSchema, &schema); err != nil || schema.Type != "object" {
			t.Errorf("%s: schema %s is not a JSON object schema (%v)", sp.Name, sp.InputSchema, err)
		}
		tl := byName[sp.Name]
		if tl == nil {
			t.Errorf("%s: not in Tools", sp.Name)
			continue
		}
		if !jsonEqual(t, json.RawMessage(sp.InputSchema), tl.InputSchema) {
			t.Errorf("%s: schema differs from Tools", sp.Name)
		}
		if sp.Title != tl.Title || sp.Description != tl.Description || sp.ReadOnly != tl.Annotations.ReadOnlyHint {
			t.Errorf("%s: metadata differs from Tools", sp.Name)
		}
		if sp.ReadOnly {
			reads++
		}
	}
	if reads != 18 {
		t.Errorf("read-only tools = %d, want 18", reads)
	}
}

func TestCallMatchesMCP(t *testing.T) {
	f := newFake()
	s := newServer(f)
	args := map[string]any{"sort": "mem", "limit": 5}
	res := call(t, connectSet(t, s, ToolSetLatest, tokenA()), "list_hosts", args)
	raw, _ := json.Marshal(args)
	got, err := s.Call(callerCtx(), "list_hosts", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(t, json.RawMessage(got), res.StructuredContent) {
		t.Errorf("Call = %s, MCP = %v", got, res.StructuredContent)
	}
	if _, err := s.Call(callerCtx(), "list_hosts", nil); err != nil {
		t.Errorf("empty args: %v", err)
	}
}

func TestCallErrors(t *testing.T) {
	s := newServer(newFake())
	for _, tc := range []struct {
		name string
		ctx  context.Context
		tool string
		args string
		want error
		text string
	}{
		{"unknown", callerCtx(), "nope", `{}`, ErrUnknownTool, ""},
		{"write tool", callerCtx(), "ack_alert", `{}`, ErrNotReadOnly, ""},
		{"wrong type", callerCtx(), "list_hosts", `{"limit":"x"}`, ErrBadInput, ""},
		{"not an object", callerCtx(), "list_hosts", `[1]`, ErrBadInput, ""},
		{"bad json", callerCtx(), "list_hosts", `{`, ErrBadInput, ""},
		{"no token", context.Background(), "list_hosts", `{}`, nil, "no token"},
	} {
		_, err := s.Call(tc.ctx, tc.tool, json.RawMessage(tc.args))
		switch {
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		case tc.want == nil && (err == nil || err.Error() != tc.text):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.text)
		}
	}
}

func TestCallAppliesDefaults(t *testing.T) {
	s := newServer(newFake())
	type in struct {
		N int `json:"n"`
	}
	type out struct {
		N int `json:"n"`
	}
	addTool(s, &sdk.Tool{
		Name:        "echo",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{"n": {Type: "integer", Default: json.RawMessage("7")}},
		},
	}, func(_ context.Context, _ *sdk.CallToolRequest, in in) (*sdk.CallToolResult, out, error) {
		return nil, out{N: in.N}, nil
	})
	got, err := s.Call(callerCtx(), "echo", json.RawMessage(`{}`))
	if err != nil || string(got) != `{"n":7}` {
		t.Fatalf("got %s, %v; want the default 7", got, err)
	}
}
