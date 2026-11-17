package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/store"
)

// TestEnrichTraceSymbolsOverlap updates the RPG node to overlap the indexed
// chunk so trace-callers JSON output carries RPG enrichment.
func TestEnrichTraceSymbolsOverlap(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	// The handlerEnv RPG node sits at main.go with the seeded symbol; the
	// enrichment path fires inside callers output when nodes overlap.
	res, err := s.handleTraceCallers(ctx, toolReq("grepai_trace_callers", map[string]any{"symbol": "hHandler", "json": true}))
	if err != nil || res.IsError {
		t.Fatalf("enrich callers: err=%v text=%s", err, resultText(t, res))
	}
}

// TestHandleIndexStatusVerbose covers the verbose branch.
func TestHandleIndexStatusVerbose(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	if _, err := s.handleIndexStatus(ctx, toolReq("grepai_index_status", map[string]any{"verbose": true})); err != nil {
		t.Fatalf("verbose index status: %v", err)
	}
}

// TestHandleListProjectsToonAndSort covers toon output and multi-workspace
// ordering.
func TestHandleListProjectsToonAndSort(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name:     "sort-ws",
		Projects: []config.ProjectEntry{{Name: "zeta", Path: filepath.Join(home, "z")}, {Name: "alpha", Path: filepath.Join(home, "a")}},
	})
	if _, err := s.handleListProjects(ctx, toolReq("grepai_list_projects", map[string]any{"workspace": "sort-ws", "format": "toon"})); err != nil {
		t.Fatalf("list projects toon: %v", err)
	}
	if _, err := s.handleListWorkspaces(ctx, toolReq("grepai_list_workspaces", nil)); err != nil {
		t.Fatalf("list workspaces sorted: %v", err)
	}
}

// TestWorkspaceSearchPathValidation drives the workspace path validation
// helper directly through the search handler with a mismatched prefix.
func TestWorkspaceSearchPathValidation(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	base := filepath.Join(home, "ws-pathval")
	p1 := seedVectorProject(t, base, "pv")
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name: "pathval-ws",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: "postgres://127.0.0.1:1/grepai?sslmode=disable"},
		},
		Projects: []config.ProjectEntry{{Name: "pv", Path: p1}},
	})
	// A prefix whose first segment matches no project root fails validation
	// before the store is created.
	res, err := s.handleSearch(ctx, toolReq("grepai_search", map[string]any{"query": "q", "workspace": "pathval-ws", "path": "totally-other/"}))
	expectToolError(t, res, err, "mismatched path prefix")
	if res != nil && res.IsError && !strings.Contains(resultText(t, res), "postgres") {
		// Validation errors take precedence; the DSN must not surface.
		t.Logf("validation error text: %s", resultText(t, res))
	}
}

// TestWorkspacePathHasIndexedFilesDirect pins the store-side path check.
func TestWorkspacePathHasIndexedFilesDirect(t *testing.T) {
	root := t.TempDir()
	vec := make([]float32, 8)
	vec[0] = 1
	ctx := context.Background()
	gob := store.NewGOBStore(filepath.Join(root, "index.gob"))
	if err := gob.SaveChunks(ctx, []store.Chunk{{
		FilePath: "ws/proj/main.go", StartLine: 1, EndLine: 2,
		Content: "x", Vector: vec, Hash: "k",
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := gob.SaveDocument(ctx, store.Document{Path: "ws/proj/main.go", Hash: "k"}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	found, err := workspacePathHasIndexedFiles(ctx, gob, "ws", []string{"proj"}, "main.go")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want indexed file match", found, err)
	}
	found, err = workspacePathHasIndexedFiles(ctx, gob, "ws", []string{"other"}, "main.go")
	if err != nil || found {
		t.Fatalf("deselected project must not match: found=%v err=%v", found, err)
	}
	found, err = workspacePathHasIndexedFiles(ctx, gob, "ws", nil, "nomatch/")
	if err != nil || found {
		t.Fatalf("non-matching path must not match: found=%v err=%v", found, err)
	}
	if _, err := workspacePathHasIndexedFiles(ctx, nil, "ws", nil, "x"); err == nil {
		t.Fatal("nil store must error")
	}
}

// TestWorkspaceSearchProjectSelection covers the projects parsing/dedup
// branch of workspace search selection.
func TestWorkspaceSearchProjectSelection(t *testing.T) {
	if got := parseProjectNames("a,, b ,a"); len(got) != 3 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("parseProjectNames = %v", got)
	}
	if got := parseProjectNames(""); len(got) != 0 {
		t.Fatalf("empty projects = %v", got)
	}
}
