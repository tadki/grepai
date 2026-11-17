package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/rpg"
	"github.com/yoanbernabeu/grepai/trace"
)

// TestCapCallGraphTruncates pins the node/edge capping contract directly:
// 60 nodes capped to 50 keep the root, drop edges of removed nodes and flag
// the graph truncated.
func TestCapCallGraphTruncates(t *testing.T) {
	g := &trace.CallGraph{Root: "hubFunc"}
	g.Nodes = map[string]trace.Symbol{"hubFunc": {Name: "hubFunc", Kind: trace.KindFunction, File: "hub.go", Line: 3, Language: "go"}}
	for i := 0; i < 60; i++ {
		name := "caller" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		g.Nodes[name] = trace.Symbol{Name: name, Kind: trace.KindFunction, File: "hub.go", Line: 10 + i, Language: "go"}
		g.Edges = append(g.Edges, trace.CallEdge{Caller: "hubFunc", Callee: name, File: "hub.go", Line: 10 + i, CallType: "call"})
	}
	capCallGraph(g, 50)
	if len(g.Nodes) != 50 {
		t.Fatalf("nodes after cap = %d, want 50", len(g.Nodes))
	}
	if !g.Truncated {
		t.Fatal("capped graph must be flagged truncated")
	}
	for _, e := range g.Edges {
		if _, ok := g.Nodes[e.Callee]; !ok {
			t.Fatalf("edge to removed callee %q survived capping", e.Callee)
		}
	}

	// Under the cap nothing changes and the flag stays false.
	g2 := &trace.CallGraph{Root: "a"}
	g2.Nodes = map[string]trace.Symbol{"a": {Name: "a"}, "b": {Name: "b"}}
	g2.Edges = []trace.CallEdge{{Caller: "a", Callee: "b", CallType: "call"}}
	capCallGraph(g2, 50)
	if len(g2.Nodes) != 2 || g2.Truncated {
		t.Fatalf("small graph must not be capped: nodes=%d truncated=%v", len(g2.Nodes), g2.Truncated)
	}
	capCallGraph(nil, 50) // nil must be a no-op
}

// TestHandleTraceGraphHubTruncation seeds a hub symbol with 60 callers so the
// graph traversal exercises the capping path end to end.
func TestHandleTraceGraphHubTruncation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "hubproj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Store.Backend = "gob"
	cfg.Trace.EnabledLanguages = []string{".go"}
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
	}

	ctx := context.Background()
	extractor := trace.NewRegexExtractor()
	symStore := trace.NewGOBSymbolStore(config.GetSymbolIndexPath(root))
	symbols := []trace.Symbol{
		{Name: "hubFunc", Kind: trace.KindFunction, File: "hub.go", Line: 3, Language: "go"},
	}
	var refs []trace.Reference
	for i := 0; i < 60; i++ {
		refs = append(refs, trace.Reference{
			SymbolName: "hubFunc", File: "hub.go", Line: 10 + i,
			Kind: trace.RefKindCall, Context: "hubFunc()",
			CallerName: "hubCaller" + string(rune('A'+i%26)) + string(rune('a'+i/26)),
			CallerFile: "hub.go", CallerLine: 10 + i,
		})
	}
	if err := symStore.SaveFileWithSignature(ctx, "hub.go", "hub-hash", extractor.Version(), symbols, refs); err != nil {
		t.Fatalf("seed hub symbols: %v", err)
	}
	if err := symStore.Persist(ctx); err != nil {
		t.Fatalf("persist hub symbols: %v", err)
	}
	_ = symStore.Close()

	s := &Server{projectRoot: root}
	res, err := s.handleTraceGraph(ctx, toolReq("grepai_trace_graph", map[string]any{"symbol": "hubFunc", "depth": 2}))
	if err != nil || res.IsError {
		t.Fatalf("hub graph: err=%v text=%s", err, resultText(t, res))
	}
	// The cap behavior is pinned deterministically on capCallGraph below;
	// here we assert the traversal answered with the root symbol.
	text := resultText(t, res)
	if !strings.Contains(text, "hubFunc") {
		t.Fatalf("hub graph text %s lacks the root symbol", text)
	}

	// Callers in compact JSON on the hub: capped call-site list.
	if _, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "hubFunc", "json": true, "compact": true})); err != nil {
		t.Fatalf("hub callers compact: %v", err)
	}
}

// TestHandleRPGExploreWithEdges seeds a connected RPG graph and drives
// explore traversal through real edges.
func TestHandleRPGExploreWithEdges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "edgeproj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
	}

	now := time.Now()
	rpgStore := rpg.NewGOBRPGStore(config.GetRPGIndexPath(root))
	g := rpgStore.GetGraph()
	g.AddNode(&rpg.Node{ID: "file:main.go", Kind: rpg.KindFile, Path: "main.go", UpdatedAt: now})
	g.AddNode(&rpg.Node{ID: "sym:main.go:edgeA", Kind: rpg.KindSymbol, SymbolName: "edgeA", Path: "main.go", UpdatedAt: now})
	g.AddNode(&rpg.Node{ID: "sym:main.go:edgeB", Kind: rpg.KindSymbol, SymbolName: "edgeB", Path: "main.go", UpdatedAt: now})
	g.AddNode(&rpg.Node{ID: "chunk:main.go:1", Kind: rpg.KindChunk, Path: "main.go", UpdatedAt: now})
	g.AddEdge(&rpg.Edge{From: "file:main.go", To: "sym:main.go:edgeA", Type: rpg.EdgeContains, UpdatedAt: now})
	g.AddEdge(&rpg.Edge{From: "file:main.go", To: "sym:main.go:edgeB", Type: rpg.EdgeContains, UpdatedAt: now})
	g.AddEdge(&rpg.Edge{From: "sym:main.go:edgeA", To: "chunk:main.go:1", Type: rpg.EdgeMapsToChunk, UpdatedAt: now})
	if err := rpgStore.Persist(context.Background()); err != nil {
		t.Fatalf("persist rpg: %v", err)
	}

	s := &Server{projectRoot: root}
	ctx := context.Background()
	res, err := s.handleRPGExplore(ctx, toolReq("grepai_rpg_explore", map[string]any{"start_node_id": "file:main.go", "depth": 2}))
	if err != nil || res.IsError {
		t.Fatalf("explore with edges: err=%v text=%s", err, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "edgeA") {
		t.Fatalf("explore text %s lacks traversed node", resultText(t, res))
	}

	// Fetch on a connected node pulls hierarchy/edges context.
	fetchRes, err := s.handleRPGFetch(ctx, toolReq("grepai_rpg_fetch", map[string]any{"node_id": "sym:main.go:edgeA"}))
	if err != nil || fetchRes.IsError {
		t.Fatalf("fetch connected node: err=%v text=%s", err, resultText(t, fetchRes))
	}

	// Search with scope filter over file nodes.
	if _, err := s.handleRPGSearch(ctx, toolReq("grepai_rpg_search", map[string]any{"query": "main", "kinds": "file,chunk", "scope": "main.go"})); err != nil {
		t.Fatalf("rpg search scoped: %v", err)
	}
}

// TestHandleWorkspaceTraceCalleesHits covers workspace callees with a real
// caller→callee edge.
func TestHandleWorkspaceTraceCalleesHits(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	base := filepath.Join(home, "ws-callee-projects")
	p1 := seedSymbolProject(t, base, "callee-proj")
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name:     "callee-ws",
		Projects: []config.ProjectEntry{{Name: "callee-proj", Path: p1}},
	})

	res, err := s.handleTraceCallees(ctx, toolReq("grepai_trace_callees", map[string]any{"symbol": "wsCaller", "workspace": "callee-ws"}))
	if err != nil || res.IsError {
		t.Fatalf("ws callees: err=%v text=%s", err, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "wsHandler") {
		t.Fatalf("ws callees text %s lacks the callee", resultText(t, res))
	}
}
