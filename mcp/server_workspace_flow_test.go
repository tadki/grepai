package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
)

// seedSymbolProject writes a project dir with a seeded gob symbol store and
// returns its root.
func seedSymbolProject(t *testing.T, base, name string) string {
	t.Helper()
	root := filepath.Join(base, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ctx := context.Background()
	extractor := trace.NewRegexExtractor()
	symStore := trace.NewGOBSymbolStore(config.GetSymbolIndexPath(root))
	symbols := []trace.Symbol{
		{Name: "wsHandler", Kind: trace.KindFunction, File: name + ".go", Line: 3, Language: "go"},
		{Name: "wsCaller", Kind: trace.KindFunction, File: name + ".go", Line: 7, Language: "go"},
	}
	refs := []trace.Reference{
		{SymbolName: "wsHandler", File: name + ".go", Line: 7, Kind: trace.RefKindCall, Context: "wsHandler()", CallerName: "wsCaller", CallerFile: name + ".go", CallerLine: 7},
	}
	if err := symStore.SaveFileWithSignature(ctx, name+".go", name+"-hash", extractor.Version(), symbols, refs); err != nil {
		t.Fatalf("seed symbols: %v", err)
	}
	if err := symStore.Persist(ctx); err != nil {
		t.Fatalf("persist symbols: %v", err)
	}
	_ = symStore.Close()
	return root
}

// seedVectorProject writes a project dir with a seeded gob vector index.
func seedVectorProject(t *testing.T, base, name string) string {
	t.Helper()
	root := filepath.Join(base, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vec := make([]float32, 8)
	vec[0] = 1
	ctx := context.Background()
	gob := store.NewGOBStore(config.GetIndexPath(root))
	if err := gob.SaveChunks(ctx, []store.Chunk{{
		FilePath: name + ".go", StartLine: 1, EndLine: 3,
		Content: "func " + name + "Body() {}", Vector: vec, Hash: name + "-hash",
	}}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	if err := gob.SaveDocument(ctx, store.Document{Path: name + ".go", Hash: name + "-hash"}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}
	if err := gob.Persist(ctx); err != nil {
		t.Fatalf("persist chunks: %v", err)
	}
	return root
}

// saveWorkspaceConfig writes a workspace.yaml under the test HOME.
func saveFlowWorkspaceConfig(t *testing.T, ws config.Workspace) {
	t.Helper()
	cfg, err := config.LoadWorkspaceConfig()
	if err != nil {
		t.Fatalf("load workspace config: %v", err)
	}
	if cfg == nil {
		cfg = config.DefaultWorkspaceConfig()
	}
	if err := cfg.AddWorkspace(ws); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(cfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}
}

// TestHandleWorkspaceTraceFlows drives trace callers/callees/graph and refs
// in workspace mode over a GOB-backed workspace (no postgres needed).
func TestHandleWorkspaceTraceFlows(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	base := filepath.Join(home, "ws-projects")
	p1 := seedSymbolProject(t, base, "alpha")
	p2 := seedSymbolProject(t, base, "beta")
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name: "flow-ws",
		Projects: []config.ProjectEntry{
			{Name: "alpha", Path: p1},
			{Name: "beta", Path: p2},
		},
	})

	for _, tool := range []string{"grepai_trace_callers", "grepai_trace_callees", "grepai_trace_graph"} {
		res, err := s.handleTraceCallers(ctx, toolReq(tool, map[string]any{"symbol": "wsHandler", "workspace": "flow-ws"}))
		_ = res
		_ = err
	}

	callers, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "wsHandler", "workspace": "flow-ws"}))
	if err != nil || callers.IsError {
		t.Fatalf("ws callers: err=%v text=%s", err, resultText(t, callers))
	}
	if !strings.Contains(resultText(t, callers), "wsCaller") {
		t.Fatalf("ws callers text %s lacks caller", resultText(t, callers))
	}
	// Scoped to one project via the project param.
	if _, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "wsHandler", "workspace": "flow-ws", "project": "alpha"})); err != nil {
		t.Fatalf("ws callers scoped: %v", err)
	}
	// Unknown project inside a known workspace.
	res, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "wsHandler", "workspace": "flow-ws", "project": "ghost"}))
	expectToolError(t, res, err, "unknown project")

	// Refs in workspace mode.
	for _, tool := range []string{"grepai_refs_readers", "grepai_refs_writers", "grepai_refs_graph"} {
		if _, err := s.handleRefsReaders(ctx, toolReq(tool, map[string]any{"symbol": "wsHandler", "workspace": "flow-ws"})); err != nil {
			t.Fatalf("ws %s: %v", tool, err)
		}
	}
}

// TestHandleIndexStatusWorkspaceGob covers the workspace branch of index
// status with a GOB-backed workspace.
func TestHandleIndexStatusWorkspaceGob(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	base := filepath.Join(home, "ws-status-projects")
	p1 := seedSymbolProject(t, base, "gamma")
	p2 := seedSymbolProject(t, base, "delta")
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name:     "status-ws",
		Projects: []config.ProjectEntry{{Name: "gamma", Path: p1}, {Name: "delta", Path: p2}},
	})

	res, err := s.handleIndexStatus(ctx, toolReq("grepai_index_status", map[string]any{"workspace": "status-ws"}))
	if err != nil || res.IsError {
		t.Fatalf("ws index status: err=%v text=%s", err, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "gamma") {
		t.Fatalf("ws index status text %s lacks projects", resultText(t, res))
	}
}

// TestHandleWorkspaceSearchValidation drives the workspace-search handler's
// validation surface: ghost workspace/project, project filtering and the
// postgres store failure path (dead DSN) after validation passes.
func TestHandleWorkspaceSearchValidation(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	base := filepath.Join(home, "ws-search-projects")
	p1 := seedVectorProject(t, base, "vec1")
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name: "search-ws",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: "postgres://127.0.0.1:1/grepai?sslmode=disable"},
		},
		Projects: []config.ProjectEntry{{Name: "vec1", Path: p1}},
	})

	args := map[string]any{"query": "q", "workspace": "search-ws"}
	res, err := s.handleSearch(ctx, toolReq("grepai_search", args))
	// The dead postgres DSN fails at store creation, after validation.
	expectToolError(t, res, err, "dead postgres workspace")

	res, err = s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q", "workspace": "search-ws", "projects": "ghost"}))
	expectToolError(t, res, err, "ghost project")
	res, err = s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q", "workspace": "search-ws", "projects": "vec1,ghost"}))
	expectToolError(t, res, err, "mixed known/ghost projects")

	// Server with a default workspace set resolves omitted workspace args.
	sWithDefault := &Server{projectRoot: s.projectRoot, workspaceName: "search-ws"}
	res, err = sWithDefault.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q"}))
	expectToolError(t, res, err, "default workspace resolution")

	// Bad path prefix inside a workspace fails validation.
	res, err = s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q", "workspace": "search-ws", "path": "/abs/path"}))
	expectToolError(t, res, err, "absolute path prefix")
}
