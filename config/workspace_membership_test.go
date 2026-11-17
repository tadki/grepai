package config

import (
	"errors"
	"path/filepath"
	"strings"
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

	got, proj, ok, err := loaded.FindOwningWorkspace(wsPath)
	if err != nil {
		t.Fatalf("FindOwningWorkspace: %v", err)
	}
	if !ok || got == nil {
		t.Fatal("expected project path to resolve to its workspace")
	}
	if got.Name != "ws-a" {
		t.Fatalf("workspace = %q, want ws-a", got.Name)
	}
	if proj == nil || proj.Name != "a" {
		t.Fatalf("project = %+v, want a", proj)
	}

	if _, _, ok, err := loaded.FindOwningWorkspace(filepath.Join(tmpDir, "other")); ok || err != nil {
		t.Fatalf("expected non-member path to resolve to no workspace, got ok=%v err=%v", ok, err)
	}
}

func TestFindOwningWorkspace_should_be_nil_safe(t *testing.T) {
	var cfg *WorkspaceConfig
	if got, _, ok, err := cfg.FindOwningWorkspace("/x"); got != nil || ok || err != nil {
		t.Fatalf("expected nil receiver to resolve to no workspace, got %+v ok=%v err=%v", got, ok, err)
	}
}

// TestFindOwningWorkspaceAmbiguous pins the deterministic fail-loud behavior
// for a path claimed by two workspaces: an error naming every candidate in
// sorted order, never a map-iteration-order winner.
func TestFindOwningWorkspaceAmbiguous(t *testing.T) {
	shared := "/shared/project-path"
	cfg := DefaultWorkspaceConfig()
	for _, name := range []string{"zeta", "alpha"} {
		if err := cfg.AddWorkspace(Workspace{
			Name:     name,
			Store:    StoreConfig{Backend: "postgres", Postgres: PostgresConfig{DSN: "postgres://localhost:5432/" + name}},
			Projects: []ProjectEntry{{Name: name + "-proj", Path: shared}},
		}); err != nil {
			t.Fatalf("AddWorkspace(%s): %v", name, err)
		}
	}
	// Repeat the lookup many times: every attempt must produce the identical
	// error regardless of Go's randomized map iteration order.
	for i := 0; i < 50; i++ {
		ws, proj, ok, err := cfg.FindOwningWorkspace(shared)
		if err == nil {
			t.Fatalf("attempt %d: expected ambiguity error, got ok=%v ws=%v proj=%v", i, ok, ws, proj)
		}
		if ok || ws != nil || proj != nil {
			t.Fatalf("attempt %d: expected nil results on ambiguity, got ok=%v ws=%v proj=%v", i, ok, ws, proj)
		}
		if !errors.Is(err, ErrAmbiguousWorkspacePath) {
			t.Fatalf("attempt %d: error %v does not wrap ErrAmbiguousWorkspacePath", i, err)
		}
		want := "alpha, zeta"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("attempt %d: error %q does not list sorted candidates %q", i, err.Error(), want)
		}
	}
}

// TestFindOwningWorkspaceExplicit proves the explicit workspace selection path
// (GetWorkspace) is unaffected by path ambiguity: a named lookup succeeds even
// when two workspaces claim the same project path, because explicit selection
// never consults owning-workspace inference.
func TestFindOwningWorkspaceExplicit(t *testing.T) {
	shared := "/shared/project-path"
	cfg := DefaultWorkspaceConfig()
	for _, name := range []string{"zeta", "alpha"} {
		if err := cfg.AddWorkspace(Workspace{
			Name:     name,
			Store:    StoreConfig{Backend: "postgres", Postgres: PostgresConfig{DSN: "postgres://localhost:5432/" + name}},
			Projects: []ProjectEntry{{Name: name + "-proj", Path: shared}},
		}); err != nil {
			t.Fatalf("AddWorkspace(%s): %v", name, err)
		}
	}
	for _, name := range []string{"alpha", "zeta"} {
		ws, err := cfg.GetWorkspace(name)
		if err != nil {
			t.Fatalf("GetWorkspace(%s): %v", name, err)
		}
		if ws.Name != name {
			t.Fatalf("GetWorkspace(%s) returned %q", name, ws.Name)
		}
	}
	if _, err := cfg.GetWorkspace("missing"); err == nil {
		t.Fatal("expected unknown workspace name to fail")
	}
}

// TestFindOwningWorkspaceSingle covers the unambiguous single-owner path
// including a second workspace that claims a different path.
func TestFindOwningWorkspaceSingle(t *testing.T) {
	cfg := DefaultWorkspaceConfig()
	if err := cfg.AddWorkspace(Workspace{
		Name:     "solo",
		Store:    StoreConfig{Backend: "postgres", Postgres: PostgresConfig{DSN: "postgres://localhost:5432/solo"}},
		Projects: []ProjectEntry{{Name: "solo-proj", Path: "/only/path"}},
	}); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	if err := cfg.AddWorkspace(Workspace{
		Name:     "other",
		Store:    StoreConfig{Backend: "postgres", Postgres: PostgresConfig{DSN: "postgres://localhost:5432/other"}},
		Projects: []ProjectEntry{{Name: "other-proj", Path: "/different/path"}},
	}); err != nil {
		t.Fatalf("AddWorkspace: %v", err)
	}
	ws, proj, ok, err := cfg.FindOwningWorkspace("/only/path")
	if err != nil || !ok {
		t.Fatalf("expected single owner, got err=%v ok=%v", err, ok)
	}
	if ws.Name != "solo" || proj.Name != "solo-proj" {
		t.Fatalf("owner = %s/%s, want solo/solo-proj", ws.Name, proj.Name)
	}
}

// TestFindWorkspaceForPathAmbiguous pins the same fail-loud behavior for the
// containment-based lookup used by mcp-serve auto-detection.
func TestFindWorkspaceForPathAmbiguous(t *testing.T) {
	cfg := DefaultWorkspaceConfig()
	for _, name := range []string{"zeta", "alpha"} {
		if err := cfg.AddWorkspace(Workspace{
			Name:     name,
			Store:    StoreConfig{Backend: "postgres", Postgres: PostgresConfig{DSN: "postgres://localhost:5432/" + name}},
			Projects: []ProjectEntry{{Name: name + "-proj", Path: "/shared/root"}},
		}); err != nil {
			t.Fatalf("AddWorkspace(%s): %v", name, err)
		}
	}
	// Save + load through the file so the function exercises its real path.
	cleanup := setTestHomeDir(t, t.TempDir())
	defer cleanup()
	if err := SaveWorkspaceConfig(cfg); err != nil {
		t.Fatalf("SaveWorkspaceConfig: %v", err)
	}
	if _, err := LoadWorkspaceConfig(); err != nil {
		t.Fatalf("LoadWorkspaceConfig: %v", err)
	}
	for i := 0; i < 50; i++ {
		name, ws, err := FindWorkspaceForPath("/shared/root/nested/file.go")
		if !errors.Is(err, ErrAmbiguousWorkspacePath) {
			t.Fatalf("attempt %d: expected ambiguity error, got name=%q ws=%v err=%v", i, name, ws, err)
		}
		if !strings.Contains(err.Error(), "alpha, zeta") {
			t.Fatalf("attempt %d: error %q does not list sorted candidates", i, err.Error())
		}
	}
}
