package rpg

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"log"
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

// captureLog diverts the stdlib log (the loaders' warning channel, which the
// CLI surfaces on stderr) into a buffer for the test's duration.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return buf.String
}

// writeStaleRPGGob hand-crafts a stale-version gob payload containing real
// data so ErrRPGIndexOutdated (not the fresh-empty path) is exercised.
func writeStaleRPGGob(t *testing.T, root string) {
	t.Helper()
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
}

func enableRPGProject(t *testing.T, root string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
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

	readLog := captureLog(t)
	store, qe, err := LoadRPGForScope(context.Background(), "", "")
	if err != nil {
		t.Fatalf("unreachable postgres should degrade to local gob, got %v", err)
	}
	if store == nil {
		t.Fatal("expected degraded local gob store")
	}
	// The degradation must be visible to the operator, and the served graph
	// must actually be the local one — not an empty placeholder.
	if warnings := readLog(); !strings.Contains(warnings, "falling back to local RPG index") {
		t.Fatalf("expected stderr warning on degradation, got log: %q", warnings)
	}
	res, err := qe.SearchNode(context.Background(), SearchNodeRequest{Query: "seeded feature epsilon", Limit: 5})
	if err != nil || len(res) == 0 || res[0].Node.SymbolName != "epsilon" {
		t.Fatalf("degraded store must serve the local graph, got %d results err=%v", len(res), err)
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

// TestLoadWorkspaceRPG_explicit_project_should_not_fall_through_to_siblings
// pins the negative: naming a project scopes the loop to that project only,
// so an empty named project surfaces ErrRPGIndexEmpty even when a sibling
// holds data — and no implicit-selection line is logged.
func TestLoadWorkspaceRPG_explicit_project_should_not_fall_through_to_siblings(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	emptyRoot := filepath.Join(base, "proj-empty")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dataRoot := seedRPGProject(t, base, "proj-data", "kappa")
	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws-explicit",
		Projects: []config.ProjectEntry{
			{Name: "empty", Path: emptyRoot},
			{Name: "data", Path: dataRoot},
		},
	})

	readLog := captureLog(t)
	_, _, err := LoadWorkspaceRPG(context.Background(), "ws-explicit", "empty")
	if !errors.Is(err, ErrRPGIndexEmpty) {
		t.Fatalf("explicit empty project must not be served a sibling's graph, got %v", err)
	}
	if logs := readLog(); strings.Contains(logs, "first non-empty RPG index") {
		t.Fatalf("explicit selection must not log an implicit project choice: %q", logs)
	}

	store, qe, err := LoadWorkspaceRPG(context.Background(), "ws-explicit", "data")
	if err != nil {
		t.Fatalf("explicit data project should load: %v", err)
	}
	defer store.Close()
	res, err := qe.SearchNode(context.Background(), SearchNodeRequest{Query: "seeded feature kappa", Limit: 5})
	if err != nil || len(res) == 0 || res[0].Node.SymbolName != "kappa" {
		t.Fatalf("explicit data project not served, got %d results err=%v", len(res), err)
	}
}

// TestLoadWorkspaceRPG_multi_project_should_log_implicit_selection locks the
// operator-visible implicit choice on multi-project workspaces.
func TestLoadWorkspaceRPG_multi_project_should_log_implicit_selection(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	emptyRoot := filepath.Join(base, "proj-empty")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dataRoot := seedRPGProject(t, base, "proj-data", "lambda")
	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws-order",
		Projects: []config.ProjectEntry{
			{Name: "empty", Path: emptyRoot},
			{Name: "data", Path: dataRoot},
		},
	})

	readLog := captureLog(t)
	store, qe, err := LoadWorkspaceRPG(context.Background(), "ws-order", "")
	if err != nil {
		t.Fatalf("LoadWorkspaceRPG: %v", err)
	}
	defer store.Close()
	res, err := qe.SearchNode(context.Background(), SearchNodeRequest{Query: "seeded feature lambda", Limit: 5})
	if err != nil || len(res) == 0 {
		t.Fatalf("expected first non-empty project to win, got %d results err=%v", len(res), err)
	}
	if logs := readLog(); !strings.Contains(logs, `serving "data"`) {
		t.Fatalf("implicit project choice must be logged, got %q", logs)
	}
}

// TestLoadWorkspaceRPG_should_map_stale_projects_to_outdated_sentinel pins
// the workspace-level ErrRPGIndexOutdated mapping in both loop orders: a
// stale project anywhere must beat the "not enabled" sentinel when nothing
// loads, because the rebuild hint is the actionable one.
func TestLoadWorkspaceRPG_should_map_stale_projects_to_outdated_sentinel(t *testing.T) {
	for _, staleFirst := range []bool{true, false} {
		home := t.TempDir()
		scopeTestEnv(t, home)
		base := t.TempDir()
		staleRoot := filepath.Join(base, "proj-stale")
		if err := os.MkdirAll(staleRoot, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		enableRPGProject(t, staleRoot)
		writeStaleRPGGob(t, staleRoot)
		emptyRoot := filepath.Join(base, "proj-empty")
		if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		projects := []config.ProjectEntry{
			{Name: "stale", Path: staleRoot},
			{Name: "empty", Path: emptyRoot},
		}
		if !staleFirst {
			projects[0], projects[1] = projects[1], projects[0]
		}
		writeWorkspaceConfig(t, config.Workspace{Name: "ws-stale", Projects: projects})

		_, _, err := LoadWorkspaceRPG(context.Background(), "ws-stale", "")
		if !errors.Is(err, ErrRPGIndexOutdated) {
			t.Fatalf("staleFirst=%v: want ErrRPGIndexOutdated, got %v", staleFirst, err)
		}
	}
}

// TestLoadWorkspaceRPG_should_warn_and_skip_unloadable_gob_project locks the
// Warning on non-outdated gob Load failures: a corrupt index must not be
// skipped silently, and later projects must still be served.
func TestLoadWorkspaceRPG_should_warn_and_skip_unloadable_gob_project(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	junkRoot := filepath.Join(base, "proj-junk")
	if err := os.MkdirAll(junkRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	enableRPGProject(t, junkRoot)
	if err := os.WriteFile(config.GetRPGIndexPath(junkRoot), []byte("not a gob"), 0o644); err != nil {
		t.Fatalf("write junk index: %v", err)
	}
	dataRoot := seedRPGProject(t, base, "proj-data", "omicron")
	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws-junk",
		Projects: []config.ProjectEntry{
			{Name: "junk", Path: junkRoot},
			{Name: "data", Path: dataRoot},
		},
	})

	readLog := captureLog(t)
	store, qe, err := LoadWorkspaceRPG(context.Background(), "ws-junk", "")
	if err != nil {
		t.Fatalf("junk first project must not block the loadable one: %v", err)
	}
	defer store.Close()
	res, err := qe.SearchNode(context.Background(), SearchNodeRequest{Query: "seeded feature omicron", Limit: 5})
	if err != nil || len(res) == 0 {
		t.Fatalf("data project not served past junk project, got %d results err=%v", len(res), err)
	}
	if logs := readLog(); !strings.Contains(logs, "skipping RPG for project junk") {
		t.Fatalf("corrupt gob Load failure must be logged as a warning, got %q", logs)
	}
}

// TestReadOnlyStore_should_not_persist_in_memory_mutations is the negative
// half of the zero-write contract: a caller mutating the loaded graph and
// then persisting/closing must leave the on-disk index byte-identical and
// reload without the mutation.
func TestReadOnlyStore_should_not_persist_in_memory_mutations(t *testing.T) {
	base := t.TempDir()
	root := seedRPGProject(t, base, "proj", "pi")
	indexPath := config.GetRPGIndexPath(root)
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read seeded index: %v", err)
	}

	store := NewReadOnlyGOBRPGStore(indexPath)
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	store.GetGraph().AddNode(&Node{ID: "sym:x.go:mutant", Kind: KindSymbol, SymbolName: "mutant"})
	if err := store.Persist(context.Background()); err != nil {
		t.Fatalf("read-only persist: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("read-only close: %v", err)
	}

	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index after close: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only store persisted in-memory mutations to disk")
	}

	reload := NewReadOnlyGOBRPGStore(indexPath)
	if err := reload.Load(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reload.GetGraph().Stats().TotalNodes; got != 1 {
		t.Fatalf("mutant leaked to disk: TotalNodes = %d, want 1", got)
	}
}

// TestLoadRPGForScope_auto_pg_workspace_empty_row_should_not_degrade_to_local
// (DSN-gated) pins the passthrough in the auto-attribution path: when the
// shared postgres store answers "nothing here yet", the loader must surface
// the sentinel instead of silently substituting a possibly stale local graph.
func TestLoadRPGForScope_auto_pg_workspace_empty_row_should_not_degrade_to_local(t *testing.T) {
	dsn := os.Getenv("GREPAI_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("GREPAI_POSTGRES_TEST_DSN is not set")
	}
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	// The local graph is fresh and non-empty on purpose: a wrong fallback
	// would serve it, and only a strict sentinel passthrough rejects it.
	root := seedRPGProject(t, base, "proj", "nu")
	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws_hardener_scope",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: dsn},
		},
		Projects: []config.ProjectEntry{{Name: "proj_empty", Path: root}},
	})
	withTestWorkingDir(t, root)

	// Pin the shared row to an empty payload so reruns are idempotent even
	// if a previous run crashed after seeding data.
	pin, err := NewPostgresRPGStore(context.Background(), dsn, "ws_hardener_scope", "proj_empty")
	if err != nil {
		t.Fatalf("pin store: %v", err)
	}
	if err := pin.Persist(context.Background()); err != nil {
		t.Fatalf("pin empty row: %v", err)
	}
	pin.Close()

	store, _, err := LoadRPGForScope(context.Background(), "", "")
	if store != nil || !errors.Is(err, ErrRPGIndexEmpty) {
		t.Fatalf("shared store answered empty; auto mode must not substitute the local graph, got store=%v err=%v", store, err)
	}
}
