package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/yoanbernabeu/grepai/config"
)

// newRegisteredTestServer builds a fully registered MCP server so tool schemas
// can be inspected exactly as an MCP client would see them via tools/list.
func newRegisteredTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return s
}

// propertyDescription extracts a parameter's description from a tool's input
// schema as an MCP client sees it.
func propertyDescription(t *testing.T, s *Server, toolName, param string) string {
	t.Helper()
	tools := s.mcpServer.ListTools()
	st, ok := tools[toolName]
	if !ok {
		t.Fatalf("tool %q not registered", toolName)
	}
	prop, ok := st.Tool.InputSchema.Properties[param].(map[string]any)
	if !ok {
		t.Fatalf("tool %q has no %q property", toolName, param)
	}
	desc, _ := prop["description"].(string)
	return desc
}

// The grepai_search path example used to be self-contradictory: the prose said
// path is project-relative but the example showed a project-prefixed path
// ('MM32/src'), which the server rejects with path_not_within_selected_project.
func TestWorkspaceSearchToolDescription_ShouldNotSuggestProjectPrefixedPath(t *testing.T) {
	s := newRegisteredTestServer(t)
	desc := propertyDescription(t, s, "grepai_search", "path")

	if strings.Contains(desc, "MM32/src") {
		t.Errorf("path description still contains the misleading project-prefixed example: %q", desc)
	}
	if !strings.Contains(desc, "relative to") {
		t.Errorf("path description should state that path is relative to a project root: %q", desc)
	}
}

// Baseline 164eb79 has 10 cross-project workspace parameters; every one must
// tell the caller how to discover valid workspace names instead of guessing.
func TestWorkspaceParamDescriptions_ShouldPointToListWorkspaces(t *testing.T) {
	s := newRegisteredTestServer(t)
	tools := s.mcpServer.ListTools()

	count := 0
	for _, st := range tools {
		prop, ok := st.Tool.InputSchema.Properties["workspace"].(map[string]any)
		if !ok {
			continue
		}
		desc, _ := prop["description"].(string)
		if !strings.Contains(desc, "Workspace name for cross-project") {
			continue
		}
		count++
		if !strings.Contains(desc, "grepai_list_workspaces") {
			t.Errorf("tool %q workspace description lacks grepai_list_workspaces discovery hint: %q", st.Tool.Name, desc)
		}
	}
	if count < 10 {
		t.Fatalf("expected at least 10 cross-project workspace parameters, found %d", count)
	}
}

// workspaceNotFoundErrorFixture installs a workspace config with two known
// workspaces so that "not found" errors can be checked for actionable output.
func workspaceNotFoundErrorFixture(t *testing.T) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	cfg := &config.WorkspaceConfig{
		Version: 1,
		Workspaces: map[string]config.Workspace{
			"alpha": {
				Name: "alpha",
				Projects: []config.ProjectEntry{
					{Name: "api", Path: "/tmp/alpha-api"},
				},
			},
			"beta": {
				Name: "beta",
				Projects: []config.ProjectEntry{
					{Name: "web", Path: "/tmp/beta-web"},
				},
			},
		},
	}
	if err := config.SaveWorkspaceConfig(cfg); err != nil {
		t.Fatalf("failed to save workspace config: %v", err)
	}
}

// assertActionableWorkspaceError checks the error carries the offending name,
// the valid names, and the discovery tool hint, so the caller can self-correct.
func assertActionableWorkspaceError(t *testing.T, result *mcp.CallToolResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected tool error result, got %+v", result)
	}
	if len(result.Content) == 0 {
		t.Fatal("error result has no content")
	}
	textContent, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	text := textContent.Text
	for _, want := range []string{"no-such", "alpha", "beta", "grepai_list_workspaces"} {
		if !strings.Contains(text, want) {
			t.Errorf("error message %q does not mention %q", text, want)
		}
	}
}

func TestWorkspaceNotFoundError_ShouldListValidNames(t *testing.T) {
	workspaceNotFoundErrorFixture(t)
	s := newRegisteredTestServer(t)
	ctx := context.Background()

	t.Run("workspace search", func(t *testing.T) {
		result, err := s.handleWorkspaceSearch(ctx, "needle", 10, false, "json", "", "no-such", "")
		assertActionableWorkspaceError(t, result, err)
	})

	t.Run("index status", func(t *testing.T) {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"workspace": "no-such"}}}
		result, err := s.handleIndexStatus(ctx, req)
		assertActionableWorkspaceError(t, result, err)
	})

	t.Run("list projects", func(t *testing.T) {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"workspace": "no-such"}}}
		result, err := s.handleListProjects(ctx, req)
		assertActionableWorkspaceError(t, result, err)
	})
}
