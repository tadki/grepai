package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/store"
)

const deadPostgresDSN = "postgres://127.0.0.1:1/grepai_test?sslmode=disable"

func postgresWorkspace(project config.ProjectEntry) *config.Workspace {
	return &config.Workspace{
		Name: "ws-fail",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: deadPostgresDSN},
		},
		Projects: []config.ProjectEntry{project},
	}
}

// TestWorkspaceWatchSelectedConfigFail pins that a present-but-broken project
// config aborts startup naming the project and stage, instead of silently
// substituting defaults (which would change the ignore face and chunking of a
// selected project).
func TestWorkspaceWatchSelectedConfigFail(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".grepai", "config.yaml"), []byte("embedder: [broken"), 0o644); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	ws := postgresWorkspace(config.ProjectEntry{Name: "broken-cfg", Path: proj})

	_, _, err := initializeWorkspaceRuntimes(context.Background(), ws, nil, nil, true)
	if err == nil {
		t.Fatal("expected startup to fail on broken project config")
	}
	for _, want := range []string{`project "broken-cfg"`, "config stage"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err.Error(), want)
		}
	}
}

// TestWorkspaceWatchNoSilentFallbackPostgresToGob pins that a selected
// project with an unreachable postgres backend aborts at the symbol store
// stage even when a local GOB symbol index exists on disk — the previous
// behavior warned and silently degraded to GOB, which serving hosts would
// never see.
func TestWorkspaceWatchNoSilentFallbackPostgresToGob(t *testing.T) {
	proj := t.TempDir()
	gobPath := config.GetSymbolIndexPath(proj)
	if err := os.MkdirAll(filepath.Dir(gobPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(gobPath, []byte("pre-existing-gob"), 0o644); err != nil {
		t.Fatalf("write gob: %v", err)
	}
	ws := postgresWorkspace(config.ProjectEntry{Name: "pg-proj", Path: proj})

	_, _, err := initializeWorkspaceRuntimes(context.Background(), ws, nil, nil, true)
	if err == nil {
		t.Fatal("expected startup to fail on unreachable postgres symbol store")
	}
	if !strings.Contains(err.Error(), "symbol store stage") {
		t.Fatalf("error %q lacks symbol store stage", err.Error())
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("error %q lacks postgres reason", err.Error())
	}
	if strings.Contains(err.Error(), "GOB") {
		t.Fatalf("error %q must not mention a GOB fallback", err.Error())
	}
}

// TestWorkspaceWatchSelectedRPGFeatureModeFail pins that an explicitly
// configured RPG feature_mode=llm without endpoint/model aborts startup
// instead of silently swapping in the local extractor.
func TestWorkspaceWatchSelectedRPGFeatureModeFail(t *testing.T) {
	proj := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	cfg.RPG.FeatureMode = "llm"
	cfg.RPG.LLMEndpoint = ""
	cfg.RPG.LLMModel = ""
	if err := cfg.Save(proj); err != nil {
		t.Fatalf("save project config: %v", err)
	}
	// GOB-backed workspace so the symbol store stage succeeds and the failure
	// lands on the RPG configuration itself.
	ws := &config.Workspace{
		Name: "ws-rpg",
		Store: config.StoreConfig{Backend: "qdrant"},
		Projects: []config.ProjectEntry{{Name: "rpg-proj", Path: proj}},
	}

	shared := store.NewGOBStore(filepath.Join(proj, "shared-index.gob"))
	_, _, err := initializeWorkspaceRuntimes(context.Background(), ws, nil, shared, true)
	if err == nil {
		t.Fatal("expected startup to fail on RPG feature_mode without LLM config")
	}
	if !strings.Contains(err.Error(), "rpg stage") || !strings.Contains(err.Error(), "feature_mode") {
		t.Fatalf("error %q lacks rpg stage context", err.Error())
	}
}
