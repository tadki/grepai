package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/rpg"
	mcp "github.com/mark3labs/mcp-go/mcp"
)

// seedDisclosureProject mirrors the rpg-package test seed: a project with an
// RPG-enabled config and a non-empty gob graph.
func seedDisclosureProject(t *testing.T, base, name, symbolName string) string {
	t.Helper()
	root := filepath.Join(base, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
	}
	rpgStore := rpg.NewGOBRPGStore(config.GetRPGIndexPath(root))
	rpgStore.GetGraph().AddNode(&rpg.Node{
		ID:         "sym:" + name + ".go:" + symbolName,
		Kind:       rpg.KindSymbol,
		Feature:    "seeded feature " + symbolName,
		SymbolName: symbolName,
		Path:       name + ".go",
		UpdatedAt:  time.Now(),
	})
	if err := rpgStore.Persist(context.Background()); err != nil {
		t.Fatalf("persist seeded rpg: %v", err)
	}
	return root
}

// TestRPGImplicitDisclosureInResponse pins that grepai_rpg_search wraps its
// payload with the scope disclosure when the project was picked implicitly,
// and keeps the historical plain shape for an explicit project call.
func TestRPGImplicitDisclosureInResponse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	emptyRoot := filepath.Join(base, "proj-empty")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dataRoot := seedDisclosureProject(t, base, "proj-data", "envelopeSolver")

	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name: "ws-env",
		Projects: []config.ProjectEntry{
			{Name: "empty", Path: emptyRoot},
			{Name: "data", Path: dataRoot},
		},
	}); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}

	s := &Server{}
	ctx := context.Background()

	// Implicit pick: no project named.
	req := mcp.CallToolRequest{}
	req.Params.Name = "grepai_rpg_search"
	req.Params.Arguments = map[string]any{"query": "seeded feature envelopeSolver", "workspace": "ws-env"}
	res, err := s.handleRPGSearch(ctx, req)
	if err != nil {
		t.Fatalf("handleRPGSearch: %v", err)
	}
	if res.IsError {
		t.Fatalf("handler returned error: %+v", res)
	}
	if len(res.Content) == 0 {
		t.Fatal("no content returned")
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content type %T is not text", res.Content[0])
	}
	var envelope struct {
		Scope struct {
			Workspace  string `json:"workspace"`
			Project    string `json:"project"`
			Implicit   bool   `json:"implicit"`
			Disclosure string `json:"disclosure"`
		} `json:"scope"`
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal([]byte(text.Text), &envelope); err != nil {
		t.Fatalf("implicit response is not a scope envelope: %v\n%s", err, text.Text)
	}
	if !envelope.Scope.Implicit || envelope.Scope.Project != "data" || envelope.Scope.Workspace != "ws-env" {
		t.Fatalf("scope = %+v, want implicit ws-env/data", envelope.Scope)
	}
	if !strings.Contains(envelope.Scope.Disclosure, "Pass project explicitly") {
		t.Fatalf("disclosure %q lacks actionable guidance", envelope.Scope.Disclosure)
	}
	if len(envelope.Results) == 0 {
		t.Fatal("envelope must still carry the search results")
	}

	// Explicit pick: historical shape preserved (no scope wrapper).
	req.Params.Arguments = map[string]any{"query": "seeded feature envelopeSolver", "workspace": "ws-env", "project": "data"}
	res2, err := s.handleRPGSearch(ctx, req)
	if err != nil {
		t.Fatalf("handleRPGSearch explicit: %v", err)
	}
	text2 := res2.Content[0].(mcp.TextContent)
	if strings.Contains(text2.Text, `"scope"`) {
		t.Fatalf("explicit project response must not carry a scope wrapper: %s", text2.Text)
	}
	var results []json.RawMessage
	if err := json.Unmarshal([]byte(text2.Text), &results); err != nil {
		t.Fatalf("explicit response is not the historical array shape: %v\n%s", err, text2.Text)
	}
}

// TestRPGImplicitDisclosureHelper pins the pure wrap/unwrap behavior of the
// disclosure helper, including the explicit and nil-selection no-ops.
func TestRPGImplicitDisclosureHelper(t *testing.T) {
	payload := []string{"a"}
	if _, disclosed := rpgImplicitDisclosure(nil, "results", payload); disclosed {
		t.Fatal("nil selection must not disclose")
	}
	explicit := &rpg.ScopeSelection{Workspace: "ws", Project: "p", Implicit: false}
	if _, disclosed := rpgImplicitDisclosure(explicit, "results", payload); disclosed {
		t.Fatal("explicit selection must not disclose")
	}
	implicit := &rpg.ScopeSelection{Workspace: "ws", Project: "p", Implicit: true}
	wrapped, disclosed := rpgImplicitDisclosure(implicit, "result", payload)
	if !disclosed {
		t.Fatal("implicit selection must disclose")
	}
	envelope, ok := wrapped.(map[string]any)
	if !ok {
		t.Fatalf("wrapper = %T, want map", wrapped)
	}
	if envelope["result"] == nil || envelope["scope"] == nil {
		t.Fatalf("wrapper must carry scope and payload keys, got %v", envelope)
	}
}

// TestRPGFetchExploreImplicitDisclosure extends the envelope contract to the
// fetch and explore handlers.
func TestRPGFetchExploreImplicitDisclosure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	dataRoot := seedDisclosureProject(t, base, "proj-solo", "fetchSolver")

	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name:     "ws-fx",
		Projects: []config.ProjectEntry{{Name: "solo", Path: dataRoot}},
	}); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}

	s := &Server{}
	ctx := context.Background()

	fetchReq := mcp.CallToolRequest{}
	fetchReq.Params.Name = "grepai_rpg_fetch"
	fetchReq.Params.Arguments = map[string]any{"node_id": "sym:proj-solo.go:fetchSolver", "workspace": "ws-fx"}
	fetchRes, err := s.handleRPGFetch(ctx, fetchReq)
	if err != nil {
		t.Fatalf("handleRPGFetch: %v", err)
	}
	fetchText := fetchRes.Content[0].(mcp.TextContent).Text
	var fetchEnvelope struct {
		Scope struct {
			Workspace string `json:"workspace"`
			Project   string `json:"project"`
			Implicit  bool   `json:"implicit"`
		} `json:"scope"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(fetchText), &fetchEnvelope); err != nil {
		t.Fatalf("fetch implicit response is not a scope envelope: %v\n%s", err, fetchText)
	}
	if !fetchEnvelope.Scope.Implicit || fetchEnvelope.Scope.Project != "solo" {
		t.Fatalf("fetch scope = %+v, want implicit solo", fetchEnvelope.Scope)
	}
	if len(fetchEnvelope.Result) == 0 || !strings.Contains(string(fetchEnvelope.Result), "fetchSolver") {
		t.Fatalf("fetch implicit response lost the node payload: %s", fetchText)
	}

	exploreReq := mcp.CallToolRequest{}
	exploreReq.Params.Name = "grepai_rpg_explore"
	exploreReq.Params.Arguments = map[string]any{"start_node_id": "sym:proj-solo.go:fetchSolver", "workspace": "ws-fx"}
	exploreRes, err := s.handleRPGExplore(ctx, exploreReq)
	if err != nil {
		t.Fatalf("handleRPGExplore: %v", err)
	}
	exploreText := exploreRes.Content[0].(mcp.TextContent).Text
	var exploreEnvelope struct {
		Scope struct {
			Implicit bool `json:"implicit"`
		} `json:"scope"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(exploreText), &exploreEnvelope); err != nil {
		t.Fatalf("explore implicit response is not a scope envelope: %v\n%s", err, exploreText)
	}
	if !exploreEnvelope.Scope.Implicit || len(exploreEnvelope.Result) == 0 {
		t.Fatalf("explore scope = %+v, want implicit with payload", exploreEnvelope.Scope)
	}
}
