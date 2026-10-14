package rpg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

func TestPostgresRPGStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("GREPAI_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("GREPAI_POSTGRES_TEST_DSN is not set")
	}

	ctx := context.Background()
	writer, err := NewPostgresRPGStore(ctx, dsn, "ws_pgstore_test", "proj_a")
	if err != nil {
		t.Fatalf("failed to create writer store: %v", err)
	}
	graph := writer.GetGraph()
	graph.AddNode(&Node{ID: "sym:ui.gd:_ready", Kind: KindSymbol, Feature: "on-ready"})
	if err := writer.Persist(ctx); err != nil {
		t.Fatalf("Persist failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reader, err := NewPostgresRPGStore(ctx, dsn, "ws_pgstore_test", "proj_a")
	if err != nil {
		t.Fatalf("failed to create reader store: %v", err)
	}
	defer reader.Close()
	if err := reader.Load(ctx); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	stats := reader.GetGraph().Stats()
	if stats.TotalNodes != 1 {
		t.Fatalf("TotalNodes = %d, want 1", stats.TotalNodes)
	}
}

// TestLoadWorkspaceRPG_postgres_selection exercises the postgres branches of
// the shared workspace loader: an empty shared row yields ErrRPGIndexEmpty, a
// non-empty row wins the project selection, and a failing backend surfaces a
// reachability error rather than the empty sentinel.
func TestLoadWorkspaceRPG_postgres_selection(t *testing.T) {
	dsn := os.Getenv("GREPAI_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("GREPAI_POSTGRES_TEST_DSN is not set")
	}
	// The loader resolves workspaces from ~/.grepai/workspace.yaml — isolate
	// HOME and register the test workspace with dummy local paths (pg mode
	// never touches project files locally).
	home := t.TempDir()
	t.Setenv("HOME", home)
	wsCfg := config.DefaultWorkspaceConfig()
	if cfgErr := wsCfg.AddWorkspace(config.Workspace{
		Name: "ws_loadworkspace_test",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: dsn},
		},
		Projects: []config.ProjectEntry{
			{Name: "proj_empty", Path: filepath.Join(home, "p_empty")},
			{Name: "proj_data", Path: filepath.Join(home, "p_data")},
		},
	}); cfgErr != nil {
		t.Fatalf("add workspace: %v", cfgErr)
	}
	if cfgErr := config.SaveWorkspaceConfig(wsCfg); cfgErr != nil {
		t.Fatalf("save workspace config: %v", cfgErr)
	}

	ctx := context.Background()
	const ws = "ws_loadworkspace_test"

	// Empty row: loader reports the empty sentinel, not a reachability error.
	if store, _, err := LoadWorkspaceRPG(ctx, ws, "proj_empty"); !errors.Is(err, ErrRPGIndexEmpty) || store != nil {
		t.Fatalf("empty row: want ErrRPGIndexEmpty, got store=%v err=%v", store, err)
	}

	// Non-empty row: loader serves the postgres graph.
	writer, err := NewPostgresRPGStore(ctx, dsn, ws, "proj_data")
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}
	writer.GetGraph().AddNode(&Node{ID: "sym:probe.gd:pg_probe", Kind: KindSymbol, Feature: "pg loader probe"})
	if err := writer.Persist(ctx); err != nil {
		t.Fatalf("persist: %v", err)
	}
	writer.Close()

	store, qe, err := LoadWorkspaceRPG(ctx, ws, "proj_data")
	if err != nil {
		t.Fatalf("non-empty row: %v", err)
	}
	if _, ok := store.(*PostgresRPGStore); !ok {
		t.Fatalf("expected postgres store, got %T", store)
	}
	res, err := qe.SearchNode(ctx, SearchNodeRequest{Query: "pg loader probe", Limit: 5})
	if err != nil || len(res) == 0 {
		t.Fatalf("pg graph not served: %d results err=%v", len(res), err)
	}
	store.Close()

	// Ghost project inside a postgres workspace fails clean.
	if _, _, err := LoadWorkspaceRPG(ctx, ws, "ghost"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ghost project: want not-found error, got %v", err)
	}

	// Unknown workspace in a configured environment fails clean, not empty.
	if _, _, badErr := LoadWorkspaceRPG(ctx, "ws_loadworkspace_test_ghost", ""); !strings.Contains(badErr.Error(), "not found") {
		t.Fatalf("unknown workspace should fail clean, got %v", badErr)
	}
}
