package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

// TestHandleTraceCalleesPositive finds callees via the caller→callee edge in
// the seeded symbol store (hCaller calls hHandler).
func TestHandleTraceCalleesPositive(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	res, err := s.handleTraceCallees(ctx, toolReq("grepai_trace_callees", map[string]any{"symbol": "hCaller"}))
	if err != nil || res.IsError {
		t.Fatalf("callees: err=%v text=%s", err, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "hHandler") {
		t.Fatalf("callees text %s lacks the callee", resultText(t, res))
	}
	// TOON output branch on callers.
	if _, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "hHandler", "format": "toon"})); err != nil {
		t.Fatalf("callers toon: %v", err)
	}
	// Graph with explicit small limit and depth.
	if _, err := s.handleTraceGraph(ctx, toolReq("grepai_trace_graph", map[string]any{"symbol": "hHandler", "depth": 1, "limit": 5})); err != nil {
		t.Fatalf("graph limited: %v", err)
	}
	// Graph in toon format.
	if _, err := s.handleTraceGraph(ctx, toolReq("grepai_trace_graph", map[string]any{"symbol": "hHandler", "format": "toon"})); err != nil {
		t.Fatalf("graph toon: %v", err)
	}
}

// TestHandleRPGValidFilters covers valid kinds and edge-type filters on the
// RPG handlers.
func TestHandleRPGValidFilters(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	if _, err := s.handleRPGSearch(ctx, toolReq("grepai_rpg_search", map[string]any{"query": "seeded", "kinds": "symbol,area"})); err != nil {
		t.Fatalf("rpg search kinds: %v", err)
	}
	if _, err := s.handleRPGSearch(ctx, toolReq("grepai_rpg_search", map[string]any{"query": "seeded", "format": "toon"})); err != nil {
		t.Fatalf("rpg search toon: %v", err)
	}
	if _, err := s.handleRPGFetch(ctx, toolReq("grepai_rpg_fetch", map[string]any{"node_id": "sym:main.go:hHandler", "format": "toon"})); err != nil {
		t.Fatalf("rpg fetch toon: %v", err)
	}
	if _, err := s.handleRPGExplore(ctx, toolReq("grepai_rpg_explore", map[string]any{"start_node_id": "sym:main.go:hHandler", "edge_types": "contains,invokes", "depth": 1, "limit": 5})); err != nil {
		t.Fatalf("rpg explore edges: %v", err)
	}
}

// TestHandleWorkspaceSearchGobBackend pins that a GOB workspace is rejected
// by the workspace-search store factory (shared stores must be postgres or
// qdrant).
func TestHandleWorkspaceSearchGobBackend(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	base := filepath.Join(home, "ws-gob-search")
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name:     "gob-ws",
		Projects: []config.ProjectEntry{{Name: "g1", Path: base}},
	})
	res, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q", "workspace": "gob-ws"}))
	expectToolError(t, res, err, "gob workspace")
}

// TestHandleIndexStatusToonWorkspace covers the toon branch of the workspace
// index status.
func TestHandleIndexStatusToonWorkspace(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	base := filepath.Join(home, "ws-toon-status")
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name:     "toon-ws",
		Projects: []config.ProjectEntry{{Name: "t1", Path: base}},
	})
	if _, err := s.handleIndexStatus(ctx, toolReq("grepai_index_status", map[string]any{"workspace": "toon-ws", "format": "toon"})); err != nil {
		t.Fatalf("ws toon status: %v", err)
	}
}

// TestTitleFixWriterJSONVariants covers the JSON rewrite branches.
func TestTitleFixWriterJSONVariants(t *testing.T) {
	var buf strings.Builder
	w := &titleFixWriter{Writer: &buf}
	if _, err := w.Write([]byte(`{"jsonrpc":"2.0"}`)); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if !strings.Contains(buf.String(), "jsonrpc") {
		t.Fatalf("json lost: %q", buf.String())
	}
	buf.Reset()
	if _, err := w.Write([]byte(``)); err != nil {
		t.Fatalf("write empty: %v", err)
	}
}

// TestHandleListProjectsNotFoundHint covers the not-found discovery hint on
// list_projects for an unknown workspace.
func TestHandleListProjectsNotFoundHint(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	res, err := s.handleListProjects(ctx, toolReq("grepai_list_projects", map[string]any{"workspace": "ghost-ws"}))
	expectToolError(t, res, err, "unknown workspace list_projects")
	if !strings.Contains(resultText(t, res), "grepai_list_workspaces") {
		t.Fatalf("error text %s lacks discovery hint", resultText(t, res))
	}
}

// TestHandleListWorkspacesEmpty covers the no-config branch.
func TestHandleListWorkspacesEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &Server{}
	res, err := s.handleListWorkspaces(context.Background(), toolReq("grepai_list_workspaces", nil))
	if err != nil || res.IsError {
		t.Fatalf("empty workspaces: err=%v", err)
	}
	if strings.TrimSpace(resultText(t, res)) != "[]" {
		t.Fatalf("empty workspace list = %q, want []", resultText(t, res))
	}
}

// TestHandleWorkspaceSearchUnknownWorkspace covers the unknown-workspace
// branch of the workspace search handler.
func TestHandleWorkspaceSearchUnknownWorkspace(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	res, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q", "workspace": "ghost-ws"}))
	expectToolError(t, res, err, "unknown workspace in search")
}
