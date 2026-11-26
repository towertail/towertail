package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	// ErrUnknownTool means that the server has no tool with this name.
	ErrUnknownTool = errors.New("mcp: unknown tool")
	// ErrNotReadOnly means that Call refused a tool that can change data.
	ErrNotReadOnly = errors.New("mcp: tool is not read-only")
	// ErrBadInput means that the arguments do not match the input schema.
	ErrBadInput = errors.New("mcp: input does not match schema")
)

// ToolSpec describes one tool for a model that runs in this process.
type ToolSpec struct {
	Name        string
	Title       string
	Description string
	InputSchema json.RawMessage // JSON Schema of the input
	ReadOnly    bool
}

type toolEntry struct {
	spec   ToolSpec
	schema *jsonschema.Resolved
	run    func(ctx context.Context, args map[string]any) (json.RawMessage, error)
}

// register adds the tool of ToolSetLatest to the registry.
func (s *Server) register(e *toolEntry) {
	if _, dup := s.tools[e.spec.Name]; dup {
		panic("mcp: duplicate tool " + e.spec.Name)
	}
	s.tools[e.spec.Name] = e
	i, _ := slices.BinarySearch(s.names, e.spec.Name)
	s.names = slices.Insert(s.names, i, e.spec.Name)
}

// newToolEntry makes the registry entry of a tool. The handler runs with
// an empty request.
func newToolEntry[In, Out any](t *sdk.Tool, schema *jsonschema.Schema, resolved *jsonschema.Resolved, h sdk.ToolHandlerFor[In, Out]) *toolEntry {
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("mcp: tool %q: input schema: %v", t.Name, err))
	}
	return &toolEntry{
		spec: ToolSpec{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: raw,
			ReadOnly:    t.Annotations != nil && t.Annotations.ReadOnlyHint,
		},
		schema: resolved,
		run: func(ctx context.Context, args map[string]any) (json.RawMessage, error) {
			var in In
			if err := remarshal(args, &in); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrBadInput, err)
			}
			_, out, err := h(ctx, &sdk.CallToolRequest{}, in)
			if err != nil {
				return nil, err
			}
			return json.Marshal(out)
		},
	}
}

func remarshal(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}

// ToolSpecs gives the tools of ToolSetLatest, sorted by name. The order is
// stable, so the prompt cache prefix does not change.
func (s *Server) ToolSpecs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(s.names))
	for _, n := range s.names {
		specs = append(specs, s.tools[n].spec)
	}
	return specs
}

// Call runs a read-only tool. ctx must carry a token (authctx.WithToken) and a
// role (authctx.WithRole). Call validates args and applies the schema
// defaults, then runs the handler. A handler error comes back unchanged.
// Call records the call, with the way from WithVia.
func (s *Server) Call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	start := s.now()
	out, err := s.call(ctx, name, args)
	s.record(ctx, name, args, start, out, err)
	return out, err
}

func (s *Server) call(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	e := s.tools[name]
	if e == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
	if !e.spec.ReadOnly {
		return nil, fmt.Errorf("%w: %s", ErrNotReadOnly, name)
	}
	in := map[string]any{}
	if len(strings.TrimSpace(string(args))) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadInput, err)
		}
	}
	if in == nil {
		in = map[string]any{}
	}
	var v any = in
	if err := e.schema.ApplyDefaults(&v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	if err := e.schema.Validate(&v); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	m, _ := v.(map[string]any)
	return e.run(ctx, m)
}
