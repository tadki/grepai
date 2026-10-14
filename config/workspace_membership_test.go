package config

import (
	"path/filepath"
	"testing"
)

func TestFindOwningWorkspace_should_match_project_path(t *testing.T) {
	tmpDir := t.TempDir()
	cleanup := setTestHomeDir(t, tmpDir)
	defer cleanup()

	wsPath := filepath.Join(tmpDir, "proj-a")
	ws := Workspace{
		Name: "ws-a",
		Store: StoreConfig{
			Backend:  "postgres",
			Postgres: PostgresConfig{DSN: "postgres://localhost:5432/test"},
		},
		Projects: []ProjectEntry{{Name: "a", Path: wsPath}},
	}
	cfg := DefaultWorkspaceConfig()
	if err := cfg.AddWorkspace(ws); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := SaveWorkspaceConfig(cfg); err != nil {
		t.Fatalf("SaveWorkspaceConfig: %v", err)
	}

	loaded, err := LoadWorkspaceConfig()
	if err != nil {
		t.Fatalf("LoadWorkspaceConfig: %v", err)
	}

	got, proj, ok := loaded.FindOwningWorkspace(wsPath)
	if !ok || got == nil {
		t.Fatal("expected project path to resolve to its workspace")
	}
	if got.Name != "ws-a" {
		t.Fatalf("workspace = %q, want ws-a", got.Name)
	}
	if proj == nil || proj.Name != "a" {
		t.Fatalf("project = %+v, want a", proj)
	}

	if _, _, ok := loaded.FindOwningWorkspace(filepath.Join(tmpDir, "other")); ok {
		t.Fatal("expected non-member path to resolve to no workspace")
	}
}

func TestFindOwningWorkspace_should_be_nil_safe(t *testing.T) {
	var cfg *WorkspaceConfig
	if got, _, ok := cfg.FindOwningWorkspace("/x"); got != nil || ok {
		t.Fatal("expected nil receiver to resolve to no workspace")
	}
}
