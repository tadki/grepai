package cli

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/rpg"
)

func TestParseRPGNodeKinds_should_accept_comma_separated_kinds(t *testing.T) {
	got, err := parseRPGNodeKinds("symbol,file, chunk")
	if err != nil {
		t.Fatalf("parseRPGNodeKinds failed: %v", err)
	}
	want := []rpg.NodeKind{rpg.KindSymbol, rpg.KindFile, rpg.KindChunk}
	if len(got) != len(want) {
		t.Fatalf("got %d kinds, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kind[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseRPGNodeKinds_should_reject_invalid_kind(t *testing.T) {
	_, err := parseRPGNodeKinds("symbol,bogus")
	if err == nil {
		t.Fatal("expected invalid kind to fail")
	}
	if !strings.Contains(err.Error(), "invalid kind: bogus") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRPGEdgeTypes_should_accept_comma_separated_types(t *testing.T) {
	got, err := parseRPGEdgeTypes("contains,invokes")
	if err != nil {
		t.Fatalf("parseRPGEdgeTypes failed: %v", err)
	}
	want := []rpg.EdgeType{rpg.EdgeContains, rpg.EdgeInvokes}
	if len(got) != len(want) {
		t.Fatalf("got %d edge types, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("edge[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestValidateRPGDirection_should_reject_invalid_direction(t *testing.T) {
	if err := validateRPGDirection("sideways"); err == nil {
		t.Fatal("expected invalid direction to fail")
	} else if !strings.Contains(err.Error(), "direction must be 'forward', 'reverse', or 'both'") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeTestRPGProject(t *testing.T) string {
	t.Helper()
	projectRoot := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(projectRoot); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	store := rpg.NewGOBRPGStore(config.GetRPGIndexPath(projectRoot))
	graph := store.GetGraph()
	now := time.Now()
	graph.AddNode(&rpg.Node{ID: "area:cli", Kind: rpg.KindArea, Feature: "cli", UpdatedAt: now})
	graph.AddNode(&rpg.Node{ID: "file:cli/search.go", Kind: rpg.KindFile, Feature: "search-command", Path: "cli/search.go", UpdatedAt: now})
	graph.AddNode(&rpg.Node{ID: "sym:cli/search.go:runSearch", Kind: rpg.KindSymbol, Feature: "search codebase", SymbolName: "runSearch", Path: "cli/search.go", StartLine: 10, EndLine: 40, Language: "go", UpdatedAt: now})
	graph.AddNode(&rpg.Node{ID: "sym:cli/search.go:printSearch", Kind: rpg.KindSymbol, Feature: "print search results", SymbolName: "printSearch", Path: "cli/search.go", StartLine: 42, EndLine: 60, Language: "go", UpdatedAt: now})
	graph.AddEdge(&rpg.Edge{From: "area:cli", To: "file:cli/search.go", Type: rpg.EdgeFeatureParent, Weight: 1, UpdatedAt: now})
	graph.AddEdge(&rpg.Edge{From: "file:cli/search.go", To: "sym:cli/search.go:runSearch", Type: rpg.EdgeContains, Weight: 1, UpdatedAt: now})
	graph.AddEdge(&rpg.Edge{From: "sym:cli/search.go:runSearch", To: "sym:cli/search.go:printSearch", Type: rpg.EdgeInvokes, Weight: 1, UpdatedAt: now})
	if err := store.Persist(context.Background()); err != nil {
		t.Fatalf("failed to persist test RPG: %v", err)
	}
	return projectRoot
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get wd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
}

func TestRunRPGSearch_json_should_return_matching_nodes(t *testing.T) {
	projectRoot := writeTestRPGProject(t)
	withWorkingDir(t, projectRoot)

	oldJSON := rpgJSON
	oldTOON := rpgTOON
	oldScope := rpgSearchScope
	oldKinds := rpgSearchKinds
	oldLimit := rpgSearchLimit
	// Scope flags bind package globals; reset them so a leaked --workspace
	// from an earlier test can't redirect this test into workspace mode.
	oldWS, oldProj := rpgWorkspace, rpgProject
	defer func() {
		rpgJSON = oldJSON
		rpgTOON = oldTOON
		rpgSearchScope = oldScope
		rpgSearchKinds = oldKinds
		rpgSearchLimit = oldLimit
		rpgWorkspace = oldWS
		rpgProject = oldProj
	}()
	rpgWorkspace, rpgProject = "", ""

	rpgJSON = true
	rpgTOON = false
	rpgSearchScope = "cli"
	rpgSearchKinds = "symbol"
	rpgSearchLimit = 5

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runRPGSearch(nil, []string{"search codebase"})
	_ = w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatalf("runRPGSearch failed: %v", err)
	}

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	var decoded []rpg.SearchNodeResult
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if len(decoded) == 0 {
		t.Fatal("expected at least one RPG search result")
	}
	if decoded[0].Node.SymbolName != "runSearch" {
		t.Fatalf("first symbol = %q, want runSearch", decoded[0].Node.SymbolName)
	}
	if decoded[0].FeaturePath != "cli" {
		t.Fatalf("feature path = %q, want cli", decoded[0].FeaturePath)
	}
}

func TestRunRPGFetch_json_should_return_node_context(t *testing.T) {
	projectRoot := writeTestRPGProject(t)
	withWorkingDir(t, projectRoot)

	oldJSON := rpgJSON
	oldTOON := rpgTOON
	oldWS, oldProj := rpgWorkspace, rpgProject
	defer func() {
		rpgJSON = oldJSON
		rpgTOON = oldTOON
		rpgWorkspace = oldWS
		rpgProject = oldProj
	}()
	rpgJSON = true
	rpgTOON = false
	rpgWorkspace, rpgProject = "", ""

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runRPGFetch(nil, []string{"sym:cli/search.go:runSearch"})
	_ = w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatalf("runRPGFetch failed: %v", err)
	}

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	var decoded rpg.FetchNodeResult
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if decoded.Node == nil || decoded.Node.SymbolName != "runSearch" {
		t.Fatalf("expected runSearch node, got %+v", decoded.Node)
	}
	if decoded.FeaturePath != "cli" {
		t.Fatalf("feature path = %q, want cli", decoded.FeaturePath)
	}
	if len(decoded.Incoming) == 0 || len(decoded.Outgoing) == 0 {
		t.Fatalf("expected incoming and outgoing edges, got %d/%d", len(decoded.Incoming), len(decoded.Outgoing))
	}
}

func TestRunRPGExplore_json_should_return_neighborhood(t *testing.T) {
	projectRoot := writeTestRPGProject(t)
	withWorkingDir(t, projectRoot)

	oldJSON := rpgJSON
	oldTOON := rpgTOON
	oldDirection := rpgExploreDirection
	oldDepth := rpgExploreDepth
	oldEdgeTypes := rpgExploreEdgeTypes
	oldLimit := rpgExploreLimit
	oldWS, oldProj := rpgWorkspace, rpgProject
	defer func() {
		rpgJSON = oldJSON
		rpgTOON = oldTOON
		rpgExploreDirection = oldDirection
		rpgExploreDepth = oldDepth
		rpgExploreEdgeTypes = oldEdgeTypes
		rpgExploreLimit = oldLimit
		rpgWorkspace = oldWS
		rpgProject = oldProj
	}()

	rpgJSON = true
	rpgTOON = false
	rpgExploreDirection = "forward"
	rpgExploreDepth = 1
	rpgExploreEdgeTypes = "invokes"
	rpgWorkspace, rpgProject = "", ""
	rpgExploreLimit = 10

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runRPGExplore(nil, []string{"sym:cli/search.go:runSearch"})
	_ = w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatalf("runRPGExplore failed: %v", err)
	}

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	var decoded rpg.ExploreResult
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if decoded.StartNode == nil || decoded.StartNode.SymbolName != "runSearch" {
		t.Fatalf("expected runSearch start node, got %+v", decoded.StartNode)
	}
	if _, ok := decoded.Nodes["sym:cli/search.go:printSearch"]; !ok {
		t.Fatalf("expected explored graph to include printSearch, got keys %+v", decoded.Nodes)
	}
	if len(decoded.Edges) != 1 || decoded.Edges[0].Type != rpg.EdgeInvokes {
		t.Fatalf("expected one invokes edge, got %+v", decoded.Edges)
	}
}

func TestRootCommand_should_include_rpg_command(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"rpg"})
	if err != nil {
		t.Fatalf("root command did not find rpg: %v", err)
	}
	if cmd == nil || cmd.Name() != "rpg" {
		t.Fatalf("expected rpg command, got %#v", cmd)
	}
}

func TestRunRPGExplore_should_reject_invalid_edge_type(t *testing.T) {
	oldEdgeTypes := rpgExploreEdgeTypes
	oldDirection := rpgExploreDirection
	defer func() {
		rpgExploreEdgeTypes = oldEdgeTypes
		rpgExploreDirection = oldDirection
	}()

	rpgExploreDirection = "both"
	rpgExploreEdgeTypes = "bogus"
	err := runRPGExplore(nil, []string{"sym:missing"})
	if err == nil {
		t.Fatal("expected invalid edge type to fail")
	}
	if !strings.Contains(err.Error(), "invalid edge type: bogus") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRPGStore_should_report_disabled_or_empty_index(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = false
	if err := cfg.Save(projectRoot); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
	withWorkingDir(t, projectRoot)

	oldWS, oldProj := rpgWorkspace, rpgProject
	defer func() { rpgWorkspace = oldWS; rpgProject = oldProj }()
	rpgWorkspace, rpgProject = "", ""

	store, qe, err := loadRPGStore(context.Background())
	if store != nil || qe != nil {
		t.Fatalf("expected nil store and query engine when RPG disabled")
	}
	if err == nil || !strings.Contains(err.Error(), "RPG is not enabled or index is empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRPGWorkspaceFlags_should_require_workspace_for_project(t *testing.T) {
	oldWS, oldProj := rpgWorkspace, rpgProject
	defer func() { rpgWorkspace = oldWS; rpgProject = oldProj }()

	rpgWorkspace, rpgProject = "", "kol"
	if err := validateRPGWorkspaceFlags(); err == nil || !strings.Contains(err.Error(), "--project requires --workspace") {
		t.Fatalf("unexpected error: %v", err)
	}
	rpgWorkspace, rpgProject = "kol", "king-of-likes"
	if err := validateRPGWorkspaceFlags(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// staleRPGIndexPayload mirrors mcp.staleGobPayload: gob decodes by field
// name, so this locally declared struct crafts a stale index without the rpg
// package's unexported gobRPGData.
type staleRPGIndexPayload struct {
	Version int
	Nodes   map[string]*rpg.Node
	Edges   []*rpg.Edge
}

func TestLoadRPGStore_should_map_outdated_index_to_rebuild_hint(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(projectRoot); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(staleRPGIndexPayload{
		Version: -1,
		Nodes:   map[string]*rpg.Node{"x:1": {ID: "x:1"}},
	}); err != nil {
		t.Fatalf("failed to encode stale payload: %v", err)
	}
	indexPath := config.GetRPGIndexPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatalf("failed to mkdir .grepai: %v", err)
	}
	if err := os.WriteFile(indexPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write stale index: %v", err)
	}
	withWorkingDir(t, projectRoot)

	oldWS, oldProj := rpgWorkspace, rpgProject
	defer func() { rpgWorkspace = oldWS; rpgProject = oldProj }()
	rpgWorkspace, rpgProject = "", ""

	_, _, err := loadRPGStore(context.Background())
	if err == nil || !strings.Contains(err.Error(), "grepai watch") || !strings.Contains(err.Error(), "outdated") {
		t.Fatalf("outdated index must map to the rebuild hint, got %v", err)
	}
	if strings.Contains(err.Error(), "RPG is not enabled") {
		t.Fatalf("outdated must not collapse into the empty-index message: %v", err)
	}
}
