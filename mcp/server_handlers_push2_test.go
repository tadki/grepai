package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/rpg"
	"github.com/yoanbernabeu/grepai/stats"
	"github.com/yoanbernabeu/grepai/trace"
)

// seedChainSymbols writes a store where a→b→c→d form a call chain so deep
// graph traversals have multiple levels to walk.
func seedChainSymbols(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	extractor := trace.NewRegexExtractor()
	symStore := trace.NewGOBSymbolStore(config.GetSymbolIndexPath(root))
	var symbols []trace.Symbol
	var refs []trace.Reference
	names := []string{"chainA", "chainB", "chainC", "chainD"}
	for i, n := range names {
		symbols = append(symbols, trace.Symbol{Name: n, Kind: trace.KindFunction, File: "chain.go", Line: 1 + i, Language: "go"})
	}
	for i := 0; i < len(names)-1; i++ {
		refs = append(refs, trace.Reference{
			SymbolName: names[i+1], File: "chain.go", Line: 20 + i,
			Kind: trace.RefKindCall, Context: names[i+1] + "()",
			CallerName: names[i], CallerFile: "chain.go", CallerLine: 20 + i,
		})
	}
	if err := symStore.SaveFileWithSignature(ctx, "chain.go", "chain-hash", extractor.Version(), symbols, refs); err != nil {
		t.Fatalf("seed chain: %v", err)
	}
	if err := symStore.Persist(ctx); err != nil {
		t.Fatalf("persist chain: %v", err)
	}
	_ = symStore.Close()
}

// TestHandleTraceGraphDeepChain walks a multi-level call chain at depth 3.
func TestHandleTraceGraphDeepChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "chainproj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Store.Backend = "gob"
	cfg.Trace.EnabledLanguages = []string{".go"}
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save: %v", err)
	}
	seedChainSymbols(t, root)

	s := &Server{projectRoot: root}
	ctx := context.Background()
	for _, depth := range []float64{1, 3} {
		res, err := s.handleTraceGraph(ctx, toolReq("grepai_trace_graph", map[string]any{"symbol": "chainA", "depth": depth}))
		if err != nil || res.IsError {
			t.Fatalf("chain graph depth %v: err=%v text=%s", depth, err, resultText(t, res))
		}
		if !strings.Contains(resultText(t, res), "chainD") && depth == 3 {
			t.Fatalf("depth-3 graph %s misses chainD", resultText(t, res))
		}
	}
	// Callees down the chain in JSON.
	res, err := s.handleTraceCallees(ctx, toolReq("grepai_trace_callees", map[string]any{"symbol": "chainA", "json": true}))
	if err != nil || res.IsError {
		t.Fatalf("chain callees: err=%v", err)
	}
	if !strings.Contains(resultText(t, res), "chainB") {
		t.Fatalf("chain callees %s misses chainB", resultText(t, res))
	}
}

// TestHandleWorkspaceTraceOutputModes drives workspace trace and refs through
// toon and compact output modes over a GOB workspace.
func TestHandleWorkspaceTraceOutputModes(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	base := filepath.Join(home, "ws-modes-projects")
	p1 := seedSymbolProject(t, base, "modes")
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name:     "modes-ws",
		Projects: []config.ProjectEntry{{Name: "modes", Path: p1}},
	})

	for _, args := range []map[string]any{
		{"symbol": "wsHandler", "workspace": "modes-ws", "format": "toon"},
		{"symbol": "wsHandler", "workspace": "modes-ws", "json": true, "compact": true},
		{"symbol": "wsHandler", "workspace": "modes-ws", "json": true},
	} {
		if _, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", args)); err != nil {
			t.Fatalf("ws callers modes %v: %v", args, err)
		}
		if _, err := s.handleTraceCallees(ctx, toolReq("grepai_trace_callees", args)); err != nil {
			t.Fatalf("ws callees modes %v: %v", args, err)
		}
		if _, err := s.handleTraceGraph(ctx, toolReq("grepai_trace_graph", args)); err != nil {
			t.Fatalf("ws graph modes %v: %v", args, err)
		}
		if _, err := s.handleRefsReaders(ctx, toolReq("grepai_refs_readers", args)); err != nil {
			t.Fatalf("ws readers modes %v: %v", args, err)
		}
		if _, err := s.handleRefsWriters(ctx, toolReq("grepai_refs_writers", args)); err != nil {
			t.Fatalf("ws writers modes %v: %v", args, err)
		}
		if _, err := s.handleRefsGraph(ctx, toolReq("grepai_refs_graph", args)); err != nil {
			t.Fatalf("ws graph refs modes %v: %v", args, err)
		}
	}
}

// TestHandleRPGExploreDirections covers forward and reverse traversals.
func TestHandleRPGExploreDirections(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "direproj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save: %v", err)
	}
	now := time.Now()
	rpgStore := rpg.NewGOBRPGStore(config.GetRPGIndexPath(root))
	g := rpgStore.GetGraph()
	g.AddNode(&rpg.Node{ID: "sym:a", Kind: rpg.KindSymbol, SymbolName: "a", Path: "a.go", UpdatedAt: now})
	g.AddNode(&rpg.Node{ID: "sym:b", Kind: rpg.KindSymbol, SymbolName: "b", Path: "b.go", UpdatedAt: now})
	g.AddEdge(&rpg.Edge{From: "sym:a", To: "sym:b", Type: rpg.EdgeContains, UpdatedAt: now})
	if err := rpgStore.Persist(context.Background()); err != nil {
		t.Fatalf("persist: %v", err)
	}

	s := &Server{projectRoot: root}
	ctx := context.Background()
	for _, dir := range []string{"forward", "reverse", "both"} {
		res, err := s.handleRPGExplore(ctx, toolReq("grepai_rpg_explore", map[string]any{"start_node_id": "sym:a", "direction": dir}))
		if err != nil || res.IsError {
			t.Fatalf("explore %s: err=%v text=%s", dir, err, resultText(t, res))
		}
	}
}

// TestHandleSearchToonAndLimit covers toon output and the limit<=0 guard.
func TestHandleSearchToonAndLimit(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	if _, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "hHandler", "format": "toon"})); err != nil {
		t.Fatalf("search toon: %v", err)
	}
	if _, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "hHandler", "limit": 0})); err != nil {
		t.Fatalf("search limit 0: %v", err)
	}
	if _, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "hHandler", "limit": -3})); err != nil {
		t.Fatalf("search negative limit: %v", err)
	}
}

// TestHandleStatsHistoryToon covers the toon + history branches of stats.
func TestHandleStatsHistoryToon(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	if _, err := s.handleStats(ctx, toolReq("grepai_stats", map[string]any{"history": true, "format": "toon"})); err != nil {
		t.Fatalf("stats toon history: %v", err)
	}
}

// TestTitleFixWriterToolsList moves annotation titles to tool roots.
func TestTitleFixWriterToolsList(t *testing.T) {
	var buf bytes.Buffer
	w := &titleFixWriter{Writer: &buf}
	payload := `{"jsonrpc":"2.0","result":{"tools":[{"name":"t1","annotations":{"title":"Nice Tool"}}]}}`
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("rewritten output not json: %v\n%s", err, buf.String())
	}
	tool := out.Result.Tools[0]
	if tool["title"] != "Nice Tool" {
		t.Fatalf("title not moved to root: %v", tool)
	}
	if ann, has := tool["annotations"].(map[string]any); has {
		if _, still := ann["title"]; still {
			t.Fatalf("annotation title must be removed: %v", tool)
		}
	}
}

// TestRegisterToolsListing exercises the full registration through a real
// MCP server listing so every With* option runs.
func TestRegisterToolsListing(t *testing.T) {
	s := &Server{projectRoot: "/tmp/test-project"}
	s.mcpServer = server.NewMCPServer("grepai-test", "1.0.0")
	s.registerTools()
	tools := s.mcpServer.ListTools()
	if len(tools) != 14 {
		t.Fatalf("registered %d tools, want 14", len(tools))
	}
}

// TestFormatBytesValues pins the byte formatter across scales.
func TestFormatBytesValues(t *testing.T) {
	if formatBytes(0) == "" {
		t.Fatal("zero bytes must render")
	}
	if !strings.Contains(formatBytes(1024), "KB") {
		t.Fatalf("1024 => %q", formatBytes(1024))
	}
	if !strings.Contains(formatBytes(5*1024*1024), "MB") {
		t.Fatalf("5MB => %q", formatBytes(5*1024*1024))
	}
}

// TestWorkspacePathExamplesShapes covers empty, duplicate and nested paths.
func TestWorkspacePathExamplesShapes(t *testing.T) {
	examples := workspacePathExamples([]config.ProjectEntry{
		{Name: "a", Path: "/repos/a"},
		{Name: "a", Path: "/repos/a"},
		{Name: "b", Path: ""},
		{Name: "c", Path: "/repos/c/nested"},
	})
	seen := map[string]bool{}
	for _, e := range examples {
		if seen[e] {
			t.Fatalf("duplicate example %q", e)
		}
		seen[e] = true
	}
}

// TestRecordMCPStatsWithRecorder covers the stats recording path.
func TestRecordMCPStatsWithRecorder(t *testing.T) {
	root := t.TempDir()
	s := &Server{projectRoot: root, recorder: stats.NewRecorder(root)}
	s.recordMCPStats("search", "full", 2, "out")
	// The recorder fires asynchronously; poll briefly for the entry.
	deadline := time.Now().Add(2 * time.Second)
	var entries []stats.Entry
	var err error
	for time.Now().Before(deadline) {
		entries, err = stats.ReadAll(stats.StatsPath(root))
		if err == nil && len(entries) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || len(entries) != 1 {
		t.Fatalf("stats entries=%d err=%v, want one recorded entry", len(entries), err)
	}
}
