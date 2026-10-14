package mcp

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/rpg"
)

// seedRPGProjectForMCP creates a project with an RPG-enabled config and a
// seeded gob graph, mirroring the rpg package's seedRPGProject test fixture.
func seedRPGProjectForMCP(t *testing.T, base, name, symbolName string) string {
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
	store := rpg.NewGOBRPGStore(config.GetRPGIndexPath(root))
	now := time.Now()
	store.GetGraph().AddNode(&rpg.Node{
		ID:         "sym:" + name + ".go:" + symbolName,
		Kind:       rpg.KindSymbol,
		Feature:    "mcp probe " + symbolName,
		SymbolName: symbolName,
		Path:       name + ".go",
		UpdatedAt:  now,
	})
	if err := store.Persist(context.Background()); err != nil {
		t.Fatalf("persist seeded rpg: %v", err)
	}
	return root
}

func TestTryLoadWorkspaceRPG_should_map_empty_sentinel_to_nil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := t.TempDir()
	emptyRoot := filepath.Join(base, "proj-empty")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name:     "ws",
		Projects: []config.ProjectEntry{{Name: "empty", Path: emptyRoot}},
	}); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}

	s := &Server{}
	st, qe, err := s.tryLoadWorkspaceRPG(context.Background(), "ws", "")
	if st != nil || qe != nil || err != nil {
		t.Fatalf("empty workspace must map to nil enrichment, got st=%v err=%v", st, err)
	}
	// Unknown project fails clean inside the MCP layer too.
	_, _, errGhost := s.tryLoadWorkspaceRPG(context.Background(), "ws", "ghost")
	if errGhost == nil || !strings.Contains(errGhost.Error(), "not found") {
		t.Fatalf("ghost project must fail clean, got %v", errGhost)
	}
}

func TestTryLoadWorkspaceRPG_should_fail_clean_on_unknown_workspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// With a configured workspace present, an unknown name must error rather
	// than degrade to silent nil enrichment.
	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name:     "real",
		Projects: []config.ProjectEntry{{Name: "p", Path: filepath.Join(home, "p")}},
	}); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}
	s := &Server{}
	_, _, err := s.tryLoadWorkspaceRPG(context.Background(), "ghost", "")
	if err == nil {
		t.Fatal("unknown workspace must error")
	}
}

func TestTryLoadRPGForScope_single_project_paths(t *testing.T) {
	base := t.TempDir()
	root := seedRPGProjectForMCP(t, base, "proj", "soloSolver")
	s := &Server{projectRoot: root}

	st, qe, err := s.tryLoadRPGForScope(context.Background(), "", "")
	if err != nil || st == nil || qe == nil {
		t.Fatalf("expected seeded graph, got st=%v err=%v", st, err)
	}
	indexPath := config.GetRPGIndexPath(root)
	before, _ := os.ReadFile(indexPath)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if after, _ := os.ReadFile(indexPath); len(after) != len(before) {
		t.Fatal("MCP read path re-persisted the gob index")
	}
	_ = st.Close()

	// Disabled project maps to plain nil, not error.
	disabledRoot := filepath.Join(base, "proj-off")
	if err := os.MkdirAll(disabledRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	off := config.DefaultConfig()
	off.RPG.Enabled = false
	if err := off.Save(disabledRoot); err != nil {
		t.Fatalf("save config: %v", err)
	}
	s2 := &Server{projectRoot: disabledRoot}
	st2, qe2, err := s2.tryLoadRPGForScope(context.Background(), "", "")
	if st2 != nil || qe2 != nil || err != nil {
		t.Fatalf("disabled config must be nil-clean, got st=%v err=%v", st2, err)
	}

	// Empty project root is a nil no-op.
	s3 := &Server{}
	if st3, _, err := s3.tryLoadRPGForScope(context.Background(), "", ""); st3 != nil || err != nil {
		t.Fatalf("empty root must be no-op, got %v", err)
	}
}

// staleGobPayload hand-crafts a stale RPG index without reaching into the
// rpg package's unexported gobRPGData: gob decodes by field name, so a
// locally declared struct with matching fields decodes into it.
type staleGobPayload struct {
	Version int
	Nodes   map[string]*rpg.Node
	Edges   []*rpg.Edge
}

func writeStaleGobIndex(t *testing.T, root string) {
	t.Helper()
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(staleGobPayload{
		Version: -1,
		Nodes:   map[string]*rpg.Node{"x:1": {ID: "x:1"}},
	}); err != nil {
		t.Fatalf("encode stale payload: %v", err)
	}
	indexPath := config.GetRPGIndexPath(root)
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatalf("mkdir .grepai: %v", err)
	}
	if err := os.WriteFile(indexPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

// TestTryLoadWorkspaceRPG_postgres_failure_should_surface_error locks the
// unified-loader semantic: a backend reachability failure surfaces as a load
// error so the rpg_* tools report the real cause. The pre-refactor behavior
// flattened it into nil enrichment, making a down backend indistinguishable
// from "not enabled or empty".
func TestTryLoadWorkspaceRPG_postgres_failure_should_surface_error(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name: "ws-pg-down",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: "postgres://u:p@127.0.0.1:1/db"},
		},
		Projects: []config.ProjectEntry{{Name: "p", Path: filepath.Join(home, "p")}},
	}); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}

	s := &Server{}
	st, qe, err := s.tryLoadWorkspaceRPG(context.Background(), "ws-pg-down", "")
	if st != nil || qe != nil {
		t.Fatalf("no store should be returned on backend failure, got st=%v qe=%v", st, qe)
	}
	if err == nil {
		t.Fatal("postgres store failure must surface, got nil")
	}
	if !strings.Contains(err.Error(), "postgres store failed") {
		t.Fatalf("error should name the postgres store failure, got %v", err)
	}
	if errors.Is(err, rpg.ErrRPGIndexEmpty) {
		t.Fatal("reachability failure must not be flattened into the empty sentinel")
	}
}

// TestTryLoadWorkspaceRPG_outdated_gob_should_not_map_to_nil locks the other
// half of the MCP mapping: the outdated sentinel must survive so handlers
// can emit the "run 'grepai watch' to rebuild" hint instead of treating the
// workspace as unenriched.
func TestTryLoadWorkspaceRPG_outdated_gob_should_not_map_to_nil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := t.TempDir()
	staleRoot := filepath.Join(base, "proj-stale")
	if err := os.MkdirAll(staleRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(staleRoot); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeStaleGobIndex(t, staleRoot)

	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name:     "ws-stale",
		Projects: []config.ProjectEntry{{Name: "stale", Path: staleRoot}},
	}); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}

	s := &Server{}
	st, _, err := s.tryLoadWorkspaceRPG(context.Background(), "ws-stale", "")
	if st != nil {
		t.Fatal("no store should be returned for an outdated index")
	}
	if !errors.Is(err, rpg.ErrRPGIndexOutdated) {
		t.Fatalf("outdated sentinel must propagate for the rebuild hint, got %v", err)
	}
}
