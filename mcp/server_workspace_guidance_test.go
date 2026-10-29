package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/yoanbernabeu/grepai/config"
)

// newRegisteredTestServer builds a fully registered MCP server so tool schemas
// can be inspected exactly as an MCP client would see them via tools/list.
func newRegisteredTestServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{projectRoot: "/tmp/test-project"}
	s.mcpServer = server.NewMCPServer("grepai-test", "1.0.0")
	s.registerTools()
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
//
// Asserting only the absence of the literal 'MM32/src' is not enough: a
// description that contradicts itself another way (e.g. "relative to the
// workspace root") or that silently drops the "never a project name prefix"
// guidance would still pass. The required anchors pin the semantics the fix
// must keep: project-root-relative AND an explicit negation of project-name
// prefixes.
func TestWorkspaceSearchToolDescription_should_not_suggest_project_prefixed_path(t *testing.T) {
	s := newRegisteredTestServer(t)
	desc := propertyDescription(t, s, "grepai_search", "path")

	forbidden := []string{"MM32/src"}
	for _, f := range forbidden {
		if strings.Contains(desc, f) {
			t.Errorf("path description still contains the misleading project-prefixed example %q: %q", f, desc)
		}
	}

	required := []string{
		"relative to a project root",
		"never a project name prefix",
	}
	for _, r := range required {
		if !strings.Contains(desc, r) {
			t.Errorf("path description must state %q: %q", r, desc)
		}
	}
}

// Every registered tool that accepts a workspace parameter must tell the
// caller how to discover valid workspace names instead of guessing. The loop
// below must NOT filter by description wording: QA on 8915f3b proved that a
// prose prefix filter ('Workspace name for cross-project') hid two real
// workspace parameters (grepai_index_status, grepai_list_projects) whose
// descriptions worded it differently — the filter passed while the live
// tools/list schema was 10/12 covered.
//
// Presence of the bare tool name is weak — a description could name the tool
// yet not instruct the caller to use it first. Requiring the full discovery
// clause kills mutations that keep the token but drop the guidance.
func TestWorkspaceParamDescriptions_should_point_to_list_workspaces(t *testing.T) {
	s := newRegisteredTestServer(t)
	tools := s.mcpServer.ListTools()

	const discoveryClause = "call grepai_list_workspaces first to discover valid names"

	count := 0
	for _, st := range tools {
		prop, ok := st.Tool.InputSchema.Properties["workspace"].(map[string]any)
		if !ok {
			continue
		}
		desc, _ := prop["description"].(string)
		count++
		if !strings.Contains(desc, discoveryClause) {
			t.Errorf("tool %q workspace description lacks the discovery clause %q: %q", st.Tool.Name, discoveryClause, desc)
		}
	}
	// Baseline 164eb79 ships 12 workspace parameters (10 cross-project +
	// index_status + list_projects); the floor guards against a future tool
	// dropping its workspace property from the schema entirely.
	if count < 12 {
		t.Fatalf("expected at least 12 tools with a workspace parameter, found %d", count)
	}
}

// setupWorkspaceHome points the global grepai config directory ($HOME/.grepai)
// at a per-test temp dir so workspace configs are isolated from the host.
func setupWorkspaceHome(t *testing.T) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
}

// saveWorkspaceConfig writes a workspace config containing exactly the given
// workspace names, overwriting any previous config in the same HOME.
func saveWorkspaceConfig(t *testing.T, names ...string) {
	t.Helper()
	cfg := &config.WorkspaceConfig{
		Version:    1,
		Workspaces: make(map[string]config.Workspace, len(names)),
	}
	for _, n := range names {
		cfg.Workspaces[n] = config.Workspace{
			Name:     n,
			Projects: []config.ProjectEntry{{Name: n + "-project", Path: "/tmp/" + n}},
		}
	}
	if err := config.SaveWorkspaceConfig(cfg); err != nil {
		t.Fatalf("failed to save workspace config: %v", err)
	}
}

// assertActionableWorkspaceError checks the error carries the offending name,
// the *currently configured* valid names, the discovery tool hint and the
// "Valid workspaces:" clause, so the caller can self-correct in one round-trip.
//
// foreign is a workspace name from a different configuration that must NOT
// leak into this error; it defends against a hardcoded name list that happens
// to match one fixture instead of reading ListWorkspaces().
func assertActionableWorkspaceError(t *testing.T, result *mcp.CallToolResult, err error, validNames []string, foreign string) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned Go error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected tool error result, got %+v", result)
	}
	text := textResultPayload(t, result)

	if !strings.Contains(text, "no-such") {
		t.Errorf("error message %q does not mention the offending workspace name", text)
	}
	for _, anchor := range []string{"Valid workspaces:", "grepai_list_workspaces"} {
		if !strings.Contains(text, anchor) {
			t.Errorf("error message %q does not mention %q", text, anchor)
		}
	}
	for _, n := range validNames {
		if !strings.Contains(text, n) {
			t.Errorf("error message %q does not list configured workspace %q", text, n)
		}
	}
	if foreign != "" && strings.Contains(text, foreign) {
		t.Errorf("error message %q leaks workspace %q that is not in the current config", text, foreign)
	}
}

// registerWorkspaceErrorCases drives the three handlers that emit a
// "workspace not found" tool error, each with an unresolvable name.
func registerWorkspaceErrorCases(t *testing.T, s *Server, ctx context.Context, validNames []string, foreign string) {
	t.Helper()

	t.Run("workspace_search", func(t *testing.T) {
		result, err := s.handleWorkspaceSearch(ctx, "needle", 10, false, "json", "", "no-such", "")
		assertActionableWorkspaceError(t, result, err, validNames, foreign)
	})

	t.Run("index_status", func(t *testing.T) {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"workspace": "no-such"}}}
		result, err := s.handleIndexStatus(ctx, req)
		assertActionableWorkspaceError(t, result, err, validNames, foreign)
	})

	t.Run("list_projects", func(t *testing.T) {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{"workspace": "no-such"}}}
		result, err := s.handleListProjects(ctx, req)
		assertActionableWorkspaceError(t, result, err, validNames, foreign)
	})
}

// Two disjoint configurations are exercised in sequence. The error must list
// the workspaces currently on disk; a mutation that hardcodes one fixture's
// names (or otherwise stops reading ListWorkspaces) fails the second case,
// because 'gamma'/'delta' would be missing and 'alpha' would leak through.
func TestWorkspaceNotFoundError_should_list_valid_names(t *testing.T) {
	setupWorkspaceHome(t)
	s := newRegisteredTestServer(t)
	ctx := context.Background()

	cases := []struct {
		names   []string
		foreign string
	}{
		{names: []string{"alpha", "beta"}, foreign: ""},
		{names: []string{"gamma", "delta"}, foreign: "alpha"},
	}

	for _, cc := range cases {
		saveWorkspaceConfig(t, cc.names...)
		cc := cc
		t.Run(strings.Join(cc.names, "_"), func(t *testing.T) {
			registerWorkspaceErrorCases(t, s, ctx, cc.names, cc.foreign)
		})
	}
}
