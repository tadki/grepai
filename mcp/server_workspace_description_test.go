package mcp

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

// TestMCPWorkspaceDescription pins the tool description contract required by
// the Seed migration: omitted workspace resolves to the server default,
// search documents its cross-project scope and <workspace>/<project>/ path
// prefixes, and trace/refs/RPG tell agents to pass project explicitly (RPG
// disclosing an implicit pick in its response scope field).
func TestMCPWorkspaceDescription(t *testing.T) {
	s := &Server{projectRoot: "/tmp/test-project"}
	s.mcpServer = server.NewMCPServer("grepai-test", "1.0.0")
	s.registerTools()

	tools := s.mcpServer.ListTools()

	defaultPhrase := "server default workspace applies (from mcp-serve --workspace)"
	for _, name := range []string{
		"grepai_search",
		"grepai_trace_callers",
		"grepai_trace_callees",
		"grepai_trace_graph",
		"grepai_refs_readers",
		"grepai_refs_writers",
		"grepai_refs_graph",
		"grepai_rpg_search",
		"grepai_rpg_fetch",
		"grepai_rpg_explore",
		"grepai_index_status",
		"grepai_list_projects",
	} {
		st, ok := tools[name]
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		wsDesc := stringParamDescription(st, "workspace")
		if !strings.Contains(wsDesc, defaultPhrase) {
			t.Fatalf("%s workspace param description lacks default-workspace semantics: %q", name, wsDesc)
		}
	}

	search := tools["grepai_search"]
	if !strings.Contains(search.Tool.Description, "cross-project") {
		t.Fatalf("search description must document cross-project search: %q", search.Tool.Description)
	}
	if !strings.Contains(search.Tool.Description, "<workspace>/<project>/") {
		t.Fatalf("search description must document the workspace/project path prefix: %q", search.Tool.Description)
	}

	for _, name := range []string{"grepai_trace_callers", "grepai_trace_callees", "grepai_trace_graph", "grepai_refs_readers", "grepai_refs_writers", "grepai_refs_graph", "grepai_rpg_search", "grepai_rpg_fetch", "grepai_rpg_explore"} {
		projDesc := stringParamDescription(tools[name], "project")
		if !strings.Contains(projDesc, "Pass it explicitly") {
			t.Fatalf("%s project param description must instruct explicit passing: %q", name, projDesc)
		}
	}
	if !strings.Contains(stringParamDescription(tools["grepai_rpg_search"], "project"), "discloses") {
		t.Fatalf("rpg project description must document the implicit-pick disclosure: %q", stringParamDescription(tools["grepai_rpg_search"], "project"))
	}
}

// stringParamDescription extracts the description of one string parameter
// from a registered tool's input schema.
func stringParamDescription(st *server.ServerTool, param string) string {
	if st.Tool.InputSchema.Properties == nil {
		return ""
	}
	raw, ok := st.Tool.InputSchema.Properties[param]
	if !ok {
		return ""
	}
	propMap, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	desc, _ := propMap["description"].(string)
	return desc
}
