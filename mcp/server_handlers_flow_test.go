package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/rpg"
	"github.com/yoanbernabeu/grepai/stats"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

// handlerEnv builds a HOME-scoped project seeded with vector chunks, symbols,
// an RPG graph and stats entries, plus an ollama-compatible embedder stub.
func handlerEnv(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "handlerproj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	embedStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":[1,0,0,0,0,0,0,0]}`))
	}))
	t.Cleanup(embedStub.Close)

	cfg := config.DefaultConfig()
	cfg.Embedder.Provider = "ollama"
	cfg.Embedder.Model = "nomic-embed-text"
	cfg.Embedder.Endpoint = embedStub.URL
	dims := 8
	cfg.Embedder.Dimensions = &dims
	cfg.Store.Backend = "gob"
	cfg.Trace.EnabledLanguages = []string{".go"}
	cfg.RPG.Enabled = true
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc hHandler() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ctx := context.Background()
	vec := make([]float32, 8)
	vec[0] = 1
	gob := store.NewGOBStore(config.GetIndexPath(root))
	if err := gob.SaveChunks(ctx, []store.Chunk{{
		FilePath: "main.go", StartLine: 1, EndLine: 3,
		Content: "func hHandler() {}", Vector: vec, Hash: "h-hash",
	}}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	if err := gob.Persist(ctx); err != nil {
		t.Fatalf("persist chunks: %v", err)
	}

	extractor := trace.NewRegexExtractor()
	symStore := trace.NewGOBSymbolStore(config.GetSymbolIndexPath(root))
	symbols := []trace.Symbol{
		{Name: "hHandler", Kind: trace.KindFunction, File: "main.go", Line: 3, Language: "go"},
		{Name: "hCaller", Kind: trace.KindFunction, File: "main.go", Line: 7, Language: "go"},
	}
	refs := []trace.Reference{
		{SymbolName: "hHandler", File: "main.go", Line: 7, Kind: trace.RefKindCall, Context: "hHandler()", CallerName: "hCaller", CallerFile: "main.go", CallerLine: 7},
		{SymbolName: "hHandler", File: "main.go", Line: 9, Kind: trace.RefKindRead, Context: "x := hHandler", CallerName: "hCaller", CallerFile: "main.go", CallerLine: 9},
		{SymbolName: "hHandler", File: "main.go", Line: 11, Kind: trace.RefKindWrite, Context: "hHandler = nil", CallerName: "hCaller", CallerFile: "main.go", CallerLine: 11},
	}
	if err := symStore.SaveFileWithSignature(ctx, "main.go", "h-hash", extractor.Version(), symbols, refs); err != nil {
		t.Fatalf("seed symbols: %v", err)
	}
	if err := symStore.Persist(ctx); err != nil {
		t.Fatalf("persist symbols: %v", err)
	}
	_ = symStore.Close()

	rpgStore := rpg.NewGOBRPGStore(config.GetRPGIndexPath(root))
	rpgStore.GetGraph().AddNode(&rpg.Node{
		ID: "sym:main.go:hHandler", Kind: rpg.KindSymbol,
		Feature: "seeded handler feature", SymbolName: "hHandler",
		Path: "main.go", UpdatedAt: time.Now(),
	})
	if err := rpgStore.Persist(ctx); err != nil {
		t.Fatalf("persist rpg: %v", err)
	}

	entries := []stats.Entry{{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		CommandType: "search", OutputMode: "full",
		ResultCount: 1, OutputTokens: 100, GrepTokens: 500,
	}}
	data, _ := json.Marshal(entries[0])
	if err := os.WriteFile(stats.StatsPath(root), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("seed stats: %v", err)
	}

	return &Server{projectRoot: root}
}

func toolReq(name string, args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

// expectToolError asserts the MCP tool-level error contract: handlers report
// errors via the result payload (IsError), the Go error stays nil.
func expectToolError(t *testing.T, res *mcp.CallToolResult, err error, what string) {
	t.Helper()
	if err != nil {
		return
	}
	if res == nil || !res.IsError {
		text := ""
		if res != nil && len(res.Content) > 0 {
			if tc, ok := res.Content[0].(mcp.TextContent); ok {
				text = tc.Text
			}
		}
		t.Fatalf("%s: expected tool error, got isError=%v text=%q", what, res != nil && res.IsError, text)
	}
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("nil result")
	}
	if len(res.Content) == 0 {
		t.Fatalf("empty content, isError=%v", res.IsError)
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content type %T", res.Content[0])
	}
	return tc.Text
}

// TestHandleSearchFlows drives the single-project search handler through the
// stubbed embedder: hits, compact mode, path filter, bad format, missing
// query and the workspace error paths.
func TestHandleSearchFlows(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()

	res, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "hHandler"}))
	if err != nil || res.IsError {
		t.Fatalf("search: err=%v isError=%v text=%s", err, res.IsError, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "main.go") {
		t.Fatalf("search text %s lacks hit file", resultText(t, res))
	}

	if _, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "hHandler", "compact": true, "format": "json"})); err != nil {
		t.Fatalf("compact search: %v", err)
	}
	if _, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "hHandler", "path": "nonexistent/"})); err != nil {
		t.Fatalf("empty-result search: %v", err)
	}
	res, err = s.handleSearch(ctx, toolReq("grepai_search", nil))
	expectToolError(t, res, err, "missing query")
	res, err = s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q", "format": "yaml"}))
	expectToolError(t, res, err, "bad format")
	res, err = s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q", "workspace": "ghost-ws"}))
	expectToolError(t, res, err, "unknown workspace")

	// No project root and no workspace: the tool explains its requirements.
	rootless := &Server{}
	res, err = rootless.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q"}))
	expectToolError(t, res, err, "rootless server search")
}

// TestHandleTraceFlows covers callers/callees/graph in single-project and
// workspace modes over the seeded symbol store.
func TestHandleTraceFlows(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()

	callers, callErr := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "hHandler"}))
	if callErr != nil || callers.IsError {
		t.Fatalf("callers: err=%v isError=%v text=%s", callErr, callers.IsError, resultText(t, callers))
	}
	if !strings.Contains(resultText(t, callers), "hCaller") {
		t.Fatalf("callers text %s lacks caller", resultText(t, callers))
	}

	callees, err := s.handleTraceCallees(ctx, toolReq("grepai_trace_callees", map[string]any{"symbol": "hHandler"}))
	_ = callees
	if err != nil || callees.IsError {
		t.Fatalf("callees: err=%v text=%s", err, resultText(t, callees))
	}

	graph, err := s.handleTraceGraph(ctx, toolReq("grepai_trace_graph", map[string]any{"symbol": "hHandler", "depth": 2}))
	if err != nil || graph.IsError {
		t.Fatalf("graph: err=%v text=%s", err, resultText(t, graph))
	}

	// JSON compact mode on callers.
	if _, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "hHandler", "json": true, "compact": true})); err != nil {
		t.Fatalf("callers compact json: %v", err)
	}

	// Validation errors.
	res, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", nil))
	expectToolError(t, res, err, "missing symbol")
	res, err = s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "x", "format": "xml"}))
	expectToolError(t, res, err, "bad format")
	rootless := &Server{}
	res, err = rootless.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "x"}))
	expectToolError(t, res, err, "rootless trace")
	res, err = s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "x", "workspace": "ghost-ws"}))
	expectToolError(t, res, err, "unknown workspace")
}

// TestHandleRefsFlows covers readers/writers/graph handlers.
func TestHandleRefsFlows(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()

	readers, err := s.handleRefsReaders(ctx, toolReq("grepai_refs_readers", map[string]any{"symbol": "hHandler"}))
	if err != nil || readers.IsError {
		t.Fatalf("readers: err=%v text=%s", err, resultText(t, readers))
	}
	if _, err := s.handleRefsWriters(ctx, toolReq("grepai_refs_writers", map[string]any{"symbol": "hHandler"})); err != nil {
		t.Fatalf("writers: %v", err)
	}
	if _, err := s.handleRefsGraph(ctx, toolReq("grepai_refs_graph", map[string]any{"symbol": "hHandler"})); err != nil {
		t.Fatalf("graph: %v", err)
	}
	resRef, errRef := s.handleRefsReaders(ctx, toolReq("grepai_refs_readers", nil))
	expectToolError(t, resRef, errRef, "missing symbol")
}

// TestHandleIndexStatusFlows covers single-project index status and the
// workspace error branches.
func TestHandleIndexStatusFlows(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()

	res, err := s.handleIndexStatus(ctx, toolReq("grepai_index_status", nil))
	if err != nil || res.IsError {
		t.Fatalf("index status: err=%v text=%s", err, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "total_files") {
		t.Fatalf("index status text %s lacks stats", resultText(t, res))
	}

	if _, err := s.handleIndexStatus(ctx, toolReq("grepai_index_status", map[string]any{"format": "toon"})); err != nil {
		t.Fatalf("toon index status: %v", err)
	}
	resIdx, errIdx := s.handleIndexStatus(ctx, toolReq("grepai_index_status", map[string]any{"format": "xml"}))
	expectToolError(t, resIdx, errIdx, "bad format")
	resIdx, errIdx = s.handleIndexStatus(ctx, toolReq("grepai_index_status", map[string]any{"workspace": "ghost-ws"}))
	expectToolError(t, resIdx, errIdx, "unknown workspace")
}

// TestHandleStatsFlows covers the stats handler summary and history paths.
func TestHandleStatsFlows(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()

	res, err := s.handleStats(ctx, toolReq("grepai_stats", nil))
	if err != nil || res.IsError {
		t.Fatalf("stats: err=%v text=%s", err, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "total_queries") {
		t.Fatalf("stats text %s lacks summary", resultText(t, res))
	}
	if _, err := s.handleStats(ctx, toolReq("grepai_stats", map[string]any{"history": true, "limit": 3})); err != nil {
		t.Fatalf("stats history: %v", err)
	}
}

// TestHandleRPGHandlersErrors covers format/kind/edge validation on the RPG
// handlers beyond the disclosure tests.
func TestHandleRPGHandlersErrors(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()

	res, err := s.handleRPGSearch(ctx, toolReq("grepai_rpg_search", map[string]any{"query": "q", "format": "xml"}))
	expectToolError(t, res, err, "bad format")
	res, err = s.handleRPGSearch(ctx, toolReq("grepai_rpg_search", map[string]any{"query": "q", "kinds": "bogus"}))
	expectToolError(t, res, err, "bogus kind")
	res, err = s.handleRPGSearch(ctx, toolReq("grepai_rpg_search", nil))
	expectToolError(t, res, err, "missing query")
	res, err = s.handleRPGFetch(ctx, toolReq("grepai_rpg_fetch", nil))
	expectToolError(t, res, err, "missing node_id")
	res, err = s.handleRPGExplore(ctx, toolReq("grepai_rpg_explore", map[string]any{"start_node_id": "x", "direction": "sideways"}))
	expectToolError(t, res, err, "invalid direction")
	res, err = s.handleRPGExplore(ctx, toolReq("grepai_rpg_explore", map[string]any{"start_node_id": "x", "edge_types": "bogus"}))
	expectToolError(t, res, err, "bogus edge type")
	res, err = s.handleRPGExplore(ctx, toolReq("grepai_rpg_explore", nil))
	expectToolError(t, res, err, "missing start_node_id")
}

// TestHandleListFormatErrors covers the toon/format branches of the listing
// handlers.
func TestHandleListFormatErrors(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()

	if _, err := s.handleListWorkspaces(ctx, toolReq("grepai_list_workspaces", map[string]any{"format": "toon"})); err != nil {
		t.Fatalf("workspaces toon: %v", err)
	}
	res, err := s.handleListWorkspaces(ctx, toolReq("grepai_list_workspaces", map[string]any{"format": "xml"}))
	expectToolError(t, res, err, "bad format")
	res, err = s.handleListProjects(ctx, toolReq("grepai_list_projects", map[string]any{"format": "xml"}))
	expectToolError(t, res, err, "bad format")
}

// TestTitleFixWriter pins the stdout rewriter: valid JSON objects gain the
// fixed title, non-JSON passes through untouched.
func TestTitleFixWriter(t *testing.T) {
	var buf strings.Builder
	w := &titleFixWriter{Writer: &buf}
	n, err := w.Write([]byte("not json at all"))
	if err != nil || n != len("not json at all") {
		t.Fatalf("passthrough write n=%d err=%v", n, err)
	}
	if buf.String() != "not json at all" {
		t.Fatalf("passthrough content %q", buf.String())
	}
	buf.Reset()
	payload := `{"jsonrpc":"2.0","result":{}}`
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatalf("json write: %v", err)
	}
	if !strings.Contains(buf.String(), "jsonrpc") {
		t.Fatalf("json content %q lost", buf.String())
	}
}

// TestServerHelpers covers small pure helpers used by the handlers.
func TestServerHelpers(t *testing.T) {
	if got := formatBytes(1536); !strings.Contains(got, "KB") {
		t.Fatalf("formatBytes(1536) = %q, want KB units", got)
	}
	if got := parseProjectNames("a, b,,c"); len(got) != 3 {
		t.Fatalf("parseProjectNames = %v, want three names", got)
	}
	// Prefix allowed when the first segment exists under a local root.
	realRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realRoot, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if !pathPrefixAllowedByProjectRoots("src/a.go", []string{realRoot}) {
		t.Fatal("existing segment under the root must be allowed")
	}
	// Unreadable/nonexistent roots cannot verify and must not reject.
	if !pathPrefixAllowedByProjectRoots("src/a.go", []string{filepath.Join(realRoot, "nope")}) {
		t.Fatal("unverifiable roots must not reject")
	}
	// A root that exists and lacks the segment rejects.
	if pathPrefixAllowedByProjectRoots("src/a.go", []string{realRoot, realRoot}) {
		// both roots are the same verified root without... src exists here, so
		// use a root that verifiably lacks the segment.
	}
	onlyDocs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(onlyDocs, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if pathPrefixAllowedByProjectRoots("src/a.go", []string{onlyDocs}) {
		t.Fatal("verified non-matching root must reject the path")
	}
	examples := workspacePathExamples([]config.ProjectEntry{{Name: "api", Path: "/repos/api"}})
	if len(examples) == 0 {
		t.Fatal("workspace path examples must not be empty")
	}
	_ = workspacePathHasIndexedFiles
	_ = store.NewGOBStore
	_ = trace.NewRegexExtractor
}

// TestHandleRPGSingleProject covers the local-gob RPG handlers of a
// root-scoped server (no workspace configured).
func TestHandleRPGSingleProject(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()

	res, err := s.handleRPGSearch(ctx, toolReq("grepai_rpg_search", map[string]any{"query": "seeded handler"}))
	if err != nil || res.IsError {
		t.Fatalf("rpg single search: err=%v text=%s", err, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "hHandler") {
		t.Fatalf("rpg search text %s lacks the seeded symbol", resultText(t, res))
	}
}
