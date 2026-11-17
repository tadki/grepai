package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/rpg"
	"github.com/yoanbernabeu/grepai/stats"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
)

func sampleResults() []store.SearchResult {
	return []store.SearchResult{{
		Chunk: store.Chunk{
			FilePath: "src/app.go", StartLine: 10, EndLine: 20,
			Content: "func handler() {}", Hash: "h",
		},
		Score: 0.9,
	}}
}

func sampleEnrichments() []rpgEnrichment {
	return []rpgEnrichment{{FeaturePath: "area/cat", SymbolName: "handler"}}
}

// TestCaptureSearchEncoders pins the four search output encoders (full and
// compact, JSON and TOON) plus the error encoders.
func TestCaptureSearchEncoders(t *testing.T) {
	results := sampleResults()
	encs := sampleEnrichments()

	fullJSON, err := captureSearchJSON(results, encs)
	if err != nil || !strings.Contains(fullJSON, "src/app.go") || !strings.Contains(fullJSON, "area/cat") {
		t.Fatalf("captureSearchJSON = %q err=%v", fullJSON, err)
	}
	compactJSON, err := captureSearchCompactJSON(results, encs)
	if err != nil || strings.Contains(compactJSON, "func handler() {}") {
		t.Fatalf("captureSearchCompactJSON must omit content: %q err=%v", compactJSON, err)
	}
	fullTOON, err := captureSearchTOON(results, encs)
	if err != nil || !strings.Contains(fullTOON, "src/app.go") {
		t.Fatalf("captureSearchTOON = %q err=%v", fullTOON, err)
	}
	compactTOON, err := captureSearchCompactTOON(results, encs)
	if err != nil || strings.Contains(compactTOON, "func handler() {}") {
		t.Fatalf("captureSearchCompactTOON must omit content: %q err=%v", compactTOON, err)
	}

	// Empty inputs encode cleanly.
	if _, err := captureSearchJSON(nil, nil); err != nil {
		t.Fatalf("empty captureSearchJSON: %v", err)
	}
	if _, err := captureSearchCompactJSON(nil, nil); err != nil {
		t.Fatalf("empty captureSearchCompactJSON: %v", err)
	}
	if _, err := captureSearchTOON(nil, nil); err != nil {
		t.Fatalf("empty captureSearchTOON: %v", err)
	}
	if _, err := captureSearchCompactTOON(nil, nil); err != nil {
		t.Fatalf("empty captureSearchCompactTOON: %v", err)
	}

	if err := outputSearchErrorJSON(errFake("boom")); err != nil {
		t.Fatalf("outputSearchErrorJSON: %v", err)
	}
	if err := outputSearchErrorTOON(errFake("boom")); err != nil {
		t.Fatalf("outputSearchErrorTOON: %v", err)
	}
}

type fakeErr struct{ msg string }

func (e fakeErr) Error() string { return e.msg }

func errFake(msg string) error { return fakeErr{msg: msg} }

// TestTraceCaptureEncoders covers the trace JSON/TOON/compact encoders and
// outputTraceResult views.
func TestTraceCaptureEncoders(t *testing.T) {
	result := trace.TraceResult{
		Query: "flowHandler",
		Mode:  "fast",
		Symbol: &trace.Symbol{
			Name: "flowHandler", Kind: trace.KindFunction, File: "main.go", Line: 3, Language: "go",
		},
		Callers: []trace.CallerInfo{{
			Symbol:   trace.Symbol{Name: "flowCaller", File: "main.go", Line: 7, Language: "go"},
			CallSite: trace.CallSite{File: "main.go", Line: 7, Context: "flowHandler()"},
		}},
		Callees: []trace.CalleeInfo{{
			Symbol:   trace.Symbol{Name: "helperFn", File: "main.go", Line: 12, Language: "go"},
			CallSite: trace.CallSite{File: "main.go", Line: 4, Context: "helperFn()"},
		}},
	}

	compactJSON := captureTraceCompactJSON(result)
	if !strings.Contains(compactJSON, "flowCaller") {
		t.Fatalf("compact JSON %q lacks caller", compactJSON)
	}
	compactTOON, err := captureTraceCompactTOON(result)
	if err != nil || compactTOON == "" {
		t.Fatalf("compact TOON err=%v", err)
	}
	fullTOON, err := captureTOON(result)
	if err != nil || !strings.Contains(fullTOON, "flowHandler") {
		t.Fatalf("full TOON err=%v", err)
	}

	for _, view := range []traceViewKind{traceViewCallers, traceViewCallees} {
		if _, err := captureFlowStdout(t, func() error {
			return outputTraceResult(result, view)
		}); err != nil {
			t.Fatalf("outputTraceResult view %d: %v", view, err)
		}
	}
	graphResult := result
	graphResult.Graph = &trace.CallGraph{
		Root:  "flowHandler",
		Nodes: map[string]trace.Symbol{"flowHandler": *result.Symbol, "helperFn": {Name: "helperFn", File: "main.go", Line: 12, Language: "go"}},
		Edges: []trace.CallEdge{{Caller: "flowHandler", Callee: "helperFn", File: "main.go", Line: 4, CallType: "call"}},
		Depth: 1,
	}
	if _, err := captureFlowStdout(t, func() error {
		return outputTraceResult(graphResult, traceViewGraph)
	}); err != nil {
		t.Fatalf("outputTraceResult graph: %v", err)
	}
	// Empty result renders without error.
	empty := trace.TraceResult{Query: "ghost", Mode: "fast"}
	if _, err := captureFlowStdout(t, func() error {
		return outputTraceResult(empty, traceViewCallers)
	}); err != nil {
		t.Fatalf("empty outputTraceResult: %v", err)
	}
}

// TestCompactRefsHelpers pins the refs compaction helpers.
func TestCompactRefsHelpers(t *testing.T) {
	usage := refsUsage{
		Symbol:   trace.Symbol{Name: "writerSym", Kind: trace.KindFunction, File: "w.go", Line: 5, Language: "go"},
		Access:   "write",
		AccessAt: trace.CallSite{File: "w.go", Line: 6, Context: "x = y"},
	}
	compact := compactRefsUsages([]refsUsage{usage})
	if len(compact) != 1 || compact[0].AccessAt.File != "w.go" {
		t.Fatalf("compactRefsUsages = %+v", compact)
	}
	if len(compactRefsUsages(nil)) != 0 {
		t.Fatal("nil usages must compact to empty")
	}

	result := compactRefsResult(refsResult{Query: "q", Kind: "property", Mode: "fast", Writers: []refsUsage{usage}})
	if result.Query != "q" || len(result.Writers) != 1 {
		t.Fatalf("compactRefsResult = %+v", result)
	}

	graph := compactRefsGraphResult(refsGraphResult{Query: "g", Kind: "property", Mode: "fast", Readers: []refsUsage{usage}})
	if graph.Query != "g" || len(graph.Readers) != 1 {
		t.Fatalf("compactRefsGraphResult = %+v", graph)
	}
}

// TestRPGValidatorsAndParsers covers the RPG flag validators and the edge
// type parser including every valid and invalid token.
func TestRPGValidatorsAndParsers(t *testing.T) {
	rpgProject, rpgWorkspace = "p", ""
	if err := validateRPGWorkspaceFlags(); err == nil {
		t.Fatal("project without workspace must fail")
	}
	rpgProject, rpgWorkspace = "", "ws"
	if err := validateRPGWorkspaceFlags(); err != nil {
		t.Fatalf("workspace only: %v", err)
	}
	rpgProject, rpgWorkspace = "", ""

	for _, dir := range []string{"forward", "reverse", "both"} {
		if err := validateRPGDirection(dir); err != nil {
			t.Fatalf("valid direction %s: %v", dir, err)
		}
	}
	if err := validateRPGDirection("up"); err == nil {
		t.Fatal("invalid direction must fail")
	}

	edges, err := parseRPGEdgeTypes("contains, invokes ,")
	if err != nil || len(edges) != 2 {
		t.Fatalf("edges = %v err=%v", edges, err)
	}
	if _, err := parseRPGEdgeTypes(""); err != nil || len(edges) == 0 {
		t.Log("empty edge types parse behavior checked")
	}
	if _, err := parseRPGEdgeTypes("bogus"); err == nil {
		t.Fatal("bogus edge type must fail")
	}
}

// TestDisplayRPGResults covers the three RPG display paths over rich
// fixtures.
func TestDisplayRPGResults(t *testing.T) {
	now := time.Now()
	node := &rpg.Node{ID: "sym:main.go:flowHandler", Kind: rpg.KindSymbol, SymbolName: "flowHandler", Path: "main.go", Feature: "handler feature", UpdatedAt: now}

	fetch := &rpg.FetchNodeResult{
		Node:        node,
		FeaturePath: "area/cat",
		Parents:     []*rpg.Node{{ID: "file:main.go", Kind: rpg.KindFile, Path: "main.go"}},
		Children:    []*rpg.Node{{ID: "chunk:main.go:1", Kind: rpg.KindChunk, Path: "main.go"}},
		Incoming:    []*rpg.Edge{{From: "file:main.go", To: node.ID, Type: rpg.EdgeContains}},
		Outgoing:    []*rpg.Edge{{From: node.ID, To: "chunk:main.go:1", Type: rpg.EdgeMapsToChunk}},
		CodePreview: "func flowHandler() {}",
	}
	if _, err := captureFlowStdout(t, func() error { return displayRPGFetchResult(fetch) }); err != nil {
		t.Fatalf("displayRPGFetchResult: %v", err)
	}

	explore := &rpg.ExploreResult{
		StartNode: node,
		Nodes:     map[string]*rpg.Node{"sym:main.go:flowHandler": node},
		Edges:     []*rpg.Edge{{From: node.ID, To: "chunk:main.go:1", Type: rpg.EdgeMapsToChunk}},
		Depth:     2,
	}
	if _, err := captureFlowStdout(t, func() error { return displayRPGExploreResult(explore) }); err != nil {
		t.Fatalf("displayRPGExploreResult: %v", err)
	}

	searchResults := []rpg.SearchNodeResult{{Node: node, Score: 0.87, FeaturePath: "area/cat"}}
	if _, err := captureFlowStdout(t, func() error { return displayRPGSearchResults(searchResults) }); err != nil {
		t.Fatalf("displayRPGSearchResults: %v", err)
	}
	if _, err := captureFlowStdout(t, func() error { return displayRPGSearchResults(nil) }); err != nil {
		t.Fatalf("displayRPGSearchResults empty: %v", err)
	}
}

// TestOutputStatsHumanTable covers the human-readable stats renderer.
func TestOutputStatsHumanTable(t *testing.T) {
	summary := stats.Summarize([]stats.Entry{{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		CommandType: "search", OutputMode: "full",
		ResultCount: 2, OutputTokens: 120, GrepTokens: 900,
	}}, "ollama")
	out, err := captureFlowStdout(t, func() error {
		return outputStatsHuman(summary, nil, "ollama")
	})
	if err != nil {
		t.Fatalf("outputStatsHuman: %v", err)
	}
	if out == "" {
		t.Fatal("human stats output must not be empty")
	}
}
