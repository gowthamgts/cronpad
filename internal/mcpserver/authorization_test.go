package mcpserver

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func missingToolActions(t *testing.T, s *mcp.Server) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "action-coverage", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	result, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	declared := ToolActions()
	var missing []string
	for _, tool := range result.Tools {
		if declared[tool.Name] == "" {
			missing = append(missing, tool.Name)
		}
		delete(declared, tool.Name)
	}
	if len(declared) > 0 {
		t.Fatalf("stale action declarations: %v", declared)
	}
	return missing
}
func TestEveryToolDeclaresAction(t *testing.T) {
	s := New(nil)
	if missing := missingToolActions(t, s); len(missing) > 0 {
		t.Fatalf("tools without actions: %v", missing)
	}
	s.AddTool(&mcp.Tool{Name: "unprotected-test", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
	if missing := missingToolActions(t, s); !slices.Equal(missing, []string{"unprotected-test"}) {
		t.Fatalf("coverage failed to reject tool: %v", missing)
	}
}
