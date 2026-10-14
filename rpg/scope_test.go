package rpg

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
)

func gobEncode(buf *bytes.Buffer, v any) error {
	return gob.NewEncoder(buf).Encode(v)
}

// scopeTestEnv isolates HOME (workspace.yaml) for the test, mirroring
// config.setTestHomeDir but local to the rpg package.
func scopeTestEnv(t *testing.T, homeDir string) {
	t.Helper()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatalf("failed to set HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", oldHome) })
}

func withTestWorkingDir(t *testing.T, dir string) {
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

// seedRPGProject creates a project directory with an RPG-enabled config and a
// seeded non-empty gob-backed RPG graph, returning the root.
func seedRPGProject(t *testing.T, base, name, symbolName string) string {
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
	store := NewGOBRPGStore(config.GetRPGIndexPath(root))
	now := time.Now()
	store.GetGraph().AddNode(&Node{
		ID:         "sym:" + name + ".go:" + symbolName,
		Kind:       KindSymbol,
		Feature:    "seeded feature " + symbolName,
		SymbolName: symbolName,
		Path:       name + ".go",
		UpdatedAt:  now,
	})
	if err := store.Persist(context.Background()); err != nil {
		t.Fatalf("persist seeded rpg: %v", err)
	}
	return root
}

func writeWorkspaceConfig(t *testing.T, ws config.Workspace) {
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

func TestLoadLocalRPGForRoot_should_not_rewrite_gob_on_close(t *testing.T) {
	base := t.TempDir()
	root := seedRPGProject(t, base, "proj", "alpha")
	indexPath := config.GetRPGIndexPath(root)
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read seeded index: %v", err)
	}

	store, qe, err := LoadLocalRPGForRoot(context.Background(), root)
	if err != nil {
		t.Fatalf("LoadLocalRPGForRoot: %v", err)
	}
	defer store.Close()

	res, err := qe.SearchNode(context.Background(), SearchNodeRequest{Query: "seeded feature alpha", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("expected seeded symbol to be searchable")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index after close: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only close rewrote the on-disk gob")
	}
}

func TestLoadLocalRPGForRoot_should_return_empty_sentinel_when_disabled(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = false
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
	}

	_, _, err := LoadLocalRPGForRoot(context.Background(), root)
	if !errors.Is(err, ErrRPGIndexEmpty) {
		t.Fatalf("want ErrRPGIndexEmpty, got %v", err)
	}
}

func TestLoadLocalRPGForRoot_should_return_outdated_when_version_mismatches(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Hand-craft a stale-version payload with real data so ErrRPGIndexOutdated
	// (not the fresh-empty path) is exercised.
	var buf bytes.Buffer
	if err := gobEncode(&buf, gobRPGData{Version: CurrentRPGIndexVersion - 1, Nodes: map[string]*Node{"x:1": {ID: "x:1", Kind: KindArea}}}); err != nil {
		t.Fatalf("encode stale payload: %v", err)
	}
	indexPath := config.GetRPGIndexPath(root)
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatalf("mkdir .grepai: %v", err)
	}
	if err := os.WriteFile(indexPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	_, _, err := LoadLocalRPGForRoot(context.Background(), root)
	if !errors.Is(err, ErrRPGIndexOutdated) {
		t.Fatalf("want ErrRPGIndexOutdated, got %v", err)
	}
}

func TestLoadWorkspaceRPG_gob_backend_should_pick_first_non_empty_project(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	emptyRoot := filepath.Join(base, "proj-empty")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dataRoot := seedRPGProject(t, base, "proj-data", "betaSolver")

	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws-gob",
		Projects: []config.ProjectEntry{
			{Name: "empty", Path: emptyRoot},
			{Name: "data", Path: dataRoot},
		},
	})

	store, qe, err := LoadWorkspaceRPG(context.Background(), "ws-gob", "")
	if err != nil {
		t.Fatalf("LoadWorkspaceRPG: %v", err)
	}
	defer store.Close()
	res, err := qe.SearchNode(context.Background(), SearchNodeRequest{Query: "seeded feature betaSolver", Limit: 5})
	if err != nil || len(res) == 0 {
		t.Fatalf("expected project-data graph, got %d results err=%v", len(res), err)
	}
	if res[0].Node.SymbolName != "betaSolver" {
		t.Fatalf("symbol = %q, want betaSolver", res[0].Node.SymbolName)
	}
}

func TestLoadWorkspaceRPG_should_fail_clean_on_unknown_workspace_and_project(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	root := seedRPGProject(t, base, "proj", "gamma")
	writeWorkspaceConfig(t, config.Workspace{
		Name:     "ws",
		Projects: []config.ProjectEntry{{Name: "real", Path: root}},
	})

	_, _, err := LoadWorkspaceRPG(context.Background(), "ghost", "")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown workspace error = %v", err)
	}
	_, _, err = LoadWorkspaceRPG(context.Background(), "ws", "phantom")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown project error = %v", err)
	}
}

func TestLoadWorkspaceRPG_should_return_empty_sentinel_when_all_projects_empty(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	emptyRoot := filepath.Join(base, "proj-empty")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkspaceConfig(t, config.Workspace{
		Name:     "ws",
		Projects: []config.ProjectEntry{{Name: "empty", Path: emptyRoot}},
	})
	if _, _, err := LoadWorkspaceRPG(context.Background(), "ws", ""); !errors.Is(err, ErrRPGIndexEmpty) {
		t.Fatalf("want ErrRPGIndexEmpty, got %v", err)
	}
}

func TestLoadRPGForScope_auto_should_attribute_project_to_gob_workspace(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	root := seedRPGProject(t, base, "proj", "delta")
	writeWorkspaceConfig(t, config.Workspace{
		Name:     "ws-gob",
		Projects: []config.ProjectEntry{{Name: "proj", Path: root}},
	})
	withTestWorkingDir(t, root)

	store, _, err := LoadRPGForScope(context.Background(), "", "")
	if err != nil {
		t.Fatalf("LoadRPGForScope: %v", err)
	}
	if store == nil {
		t.Fatal("expected gob-workspace attribution to load a store")
	}
	_ = store.Close()
}

func TestLoadRPGForScope_auto_should_degrade_to_local_gob_when_postgres_unreachable(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	root := seedRPGProject(t, base, "proj", "epsilon")
	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws-pg",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: "postgres://u:p@127.0.0.1:1/db"},
		},
		Projects: []config.ProjectEntry{{Name: "proj", Path: root}},
	})
	withTestWorkingDir(t, root)

	store, _, err := LoadRPGForScope(context.Background(), "", "")
	if err != nil {
		t.Fatalf("unreachable postgres should degrade to local gob, got %v", err)
	}
	if store == nil {
		t.Fatal("expected degraded local gob store")
	}
	_ = store.Close()
}

func TestLoadRPGForScope_explicit_postgres_unreachable_should_report_clean_error(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	root := seedRPGProject(t, base, "proj", "zeta")
	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws-pg",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: "postgres://u:p@127.0.0.1:1/db"},
		},
		Projects: []config.ProjectEntry{{Name: "proj", Path: root}},
	})
	withTestWorkingDir(t, root)

	_, _, err := LoadRPGForScope(context.Background(), "ws-pg", "")
	if err == nil {
		t.Fatal("explicit --workspace with unreachable postgres must report an error, not degrade")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("error should mention postgres, got %v", err)
	}
}

func TestLoadRPGForScope_no_workspace_no_project_config_should_error_clean(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	root := seedRPGProject(t, base, "plain", "eta")
	withTestWorkingDir(t, root)

	store, _, err := LoadRPGForScope(context.Background(), "", "")
	if err != nil {
		t.Fatalf("plain single project should keep local gob behavior: %v", err)
	}
	if store == nil {
		t.Fatal("expected single-project gob store")
	}
	_ = store.Close()
}

func TestLoadWorkspaceRPG_should_handle_mixed_project_row_states(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	brokenRoot := filepath.Join(base, "proj-broken")
	if err := os.MkdirAll(filepath.Join(brokenRoot, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(brokenRoot, ".grepai", "config.yaml"), []byte("invalid: [unclosed"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	dataRoot := seedRPGProject(t, base, "proj-data", "omega")

	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws-mixed",
		Projects: []config.ProjectEntry{
			{Name: "no-path"},
			{Name: "broken", Path: brokenRoot},
			{Name: "data", Path: dataRoot},
		},
	})

	store, qe, err := LoadWorkspaceRPG(context.Background(), "ws-mixed", "")
	if err != nil {
		t.Fatalf("LoadWorkspaceRPG: %v", err)
	}
	defer store.Close()
	res, err := qe.SearchNode(context.Background(), SearchNodeRequest{Query: "seeded feature omega", Limit: 5})
	if err != nil || len(res) != 1 {
		t.Fatalf("expected data project selection, got %d results err=%v", len(res), err)
	}
	// Read-only workspace entry must not materialize files in the broken
	// project's .grepai dir.
	entries, _ := os.ReadDir(filepath.Join(brokenRoot, ".grepai"))
	if len(entries) != 1 { // only config.yaml
		t.Fatalf("broken project dir mutated: %d entries", len(entries))
	}
}

func TestLoadWorkspaceRPG_should_return_config_error_on_invalid_workspace_yaml(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	if err := config.SaveWorkspaceConfig(config.DefaultWorkspaceConfig()); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir home grepai: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grepai", "workspace.yaml"), []byte("] not yaml ["), 0o644); err != nil {
		t.Fatalf("write workspace yaml: %v", err)
	}
	_, _, err := LoadWorkspaceRPG(context.Background(), "ws", "")
	if err == nil || !strings.Contains(err.Error(), "workspace config") {
		t.Fatalf("want clean config error, got %v", err)
	}
	if errors.Is(err, ErrRPGIndexEmpty) {
		t.Fatal("config error must not be flattened into the empty sentinel")
	}
}

func TestLoadWorkspaceRPG_should_return_empty_sentinel_without_workspace_yaml(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	if _, _, err := LoadWorkspaceRPG(context.Background(), "ws", ""); !errors.Is(err, ErrRPGIndexEmpty) {
		t.Fatalf("want ErrRPGIndexEmpty, got %v", err)
	}
}

func TestLoadLocalRPGForRoot_should_wrap_gob_decode_failure(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := os.WriteFile(config.GetRPGIndexPath(root), []byte("not a gob"), 0o644); err != nil {
		t.Fatalf("write junk index: %v", err)
	}
	_, _, err := LoadLocalRPGForRoot(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "failed to load RPG") {
		t.Fatalf("want wrapped decode error, got %v", err)
	}
}

func TestLoadLocalRPGForRoot_should_wrap_config_load_failure(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".grepai", "config.yaml"), []byte("invalid: [unclosed"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, _, err := LoadLocalRPGForRoot(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "failed to load configuration") {
		t.Fatalf("want wrapped config error, got %v", err)
	}
}

func TestLoadRPGForScope_auto_should_degrade_to_local_when_workspace_config_invalid(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	root := seedRPGProject(t, base, "plain", "theta")
	withTestWorkingDir(t, root)
	if err := os.MkdirAll(filepath.Join(home, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir home grepai: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grepai", "workspace.yaml"), []byte("] not yaml ["), 0o644); err != nil {
		t.Fatalf("write workspace yaml: %v", err)
	}
	store, _, err := LoadRPGForScope(context.Background(), "", "")
	if err != nil {
		t.Fatalf("invalid workspace config should degrade to local gob: %v", err)
	}
	if store == nil {
		t.Fatal("expected degraded local store")
	}
	_ = store.Close()
}

func TestLoadRPGForScope_explicit_workspace_empty_should_not_degrade_to_local(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	seedRPGProject(t, base, "local-fresh", "iota") // project with local data
	emptyRoot := filepath.Join(base, "proj-empty")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkspaceConfig(t, config.Workspace{
		Name:     "ws-empty",
		Projects: []config.ProjectEntry{{Name: "empty", Path: emptyRoot}},
	})
	withTestWorkingDir(t, filepath.Join(base, "local-fresh"))

	_, _, err := LoadRPGForScope(context.Background(), "ws-empty", "")
	if !errors.Is(err, ErrRPGIndexEmpty) {
		t.Fatalf("explicit workspace mode must not fall back to the local graph, got %v", err)
	}
}

func TestReadOnlyStore_should_not_create_files_on_read(t *testing.T) {
	dir := t.TempDir()
	store := NewReadOnlyGOBRPGStore(filepath.Join(dir, "rpg.gob"))
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("read-only load on missing index: %v", err)
	}
	if err := store.Persist(context.Background()); err != nil {
		t.Fatalf("read-only persist: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("read-only close: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("read-only store created files: %v", names)
	}
}
