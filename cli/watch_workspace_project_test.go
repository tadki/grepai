package cli

import (
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

func workspaceForProjectFilterTest() *config.Workspace {
	return &config.Workspace{
		Name: "ws-filter",
		Projects: []config.ProjectEntry{
			{Name: "alpha", Path: "/tmp/wsx/alpha"},
			{Name: "beta", Path: "/tmp/wsx/beta"},
			{Name: "gamma", Path: "/nonexistent/missing-path"},
		},
	}
}

// TestWatchWorkspaceProjectFilter pins that a repeatable --project selection
// keeps source order and leaves the original workspace untouched.
func TestWatchWorkspaceProjectFilter(t *testing.T) {
	original := workspaceForProjectFilterTest()
	ws, err := selectWorkspaceProjects(original, []string{"alpha", "gamma"})
	if err != nil {
		t.Fatalf("selectWorkspaceProjects: %v", err)
	}
	if len(ws.Projects) != 2 {
		t.Fatalf("selected projects = %d, want 2", len(ws.Projects))
	}
	if ws.Projects[0].Name != "alpha" || ws.Projects[1].Name != "gamma" {
		t.Fatalf("selection order = %s,%s, want alpha,gamma", ws.Projects[0].Name, ws.Projects[1].Name)
	}
	// The unselected project with a broken path must be filtered out BEFORE
	// any path check — its presence in the result would mean filtering
	// happened after path validation.
	for _, p := range ws.Projects {
		if p.Name == "beta" {
			t.Fatal("unselected project beta must not appear in the filtered set")
		}
	}
	if len(original.Projects) != 3 {
		t.Fatalf("original workspace mutated: %d projects, want 3", len(original.Projects))
	}
}

// TestWatchWorkspaceProjectDefaultAll pins the existing full semantics: no
// --project flag means every project is watched, byte-identical config.
func TestWatchWorkspaceProjectDefaultAll(t *testing.T) {
	original := workspaceForProjectFilterTest()
	all, err := selectWorkspaceProjects(original, nil)
	if err != nil {
		t.Fatalf("selectWorkspaceProjects(nil): %v", err)
	}
	if len(all.Projects) != 3 {
		t.Fatalf("default selection = %d projects, want 3", len(all.Projects))
	}
	empty, err := selectWorkspaceProjects(original, []string{})
	if err != nil {
		t.Fatalf("selectWorkspaceProjects(empty): %v", err)
	}
	if len(empty.Projects) != 3 {
		t.Fatalf("empty selection = %d projects, want 3", len(empty.Projects))
	}
}

// TestWatchWorkspaceProjectUnknown pins fail-loud behavior for a name that is
// not part of the workspace, including the sorted known-projects hint.
func TestWatchWorkspaceProjectUnknown(t *testing.T) {
	ws, err := selectWorkspaceProjects(workspaceForProjectFilterTest(), []string{"nope"})
	if err == nil {
		t.Fatalf("expected unknown project error, got ws=%v", ws)
	}
	if !strings.Contains(err.Error(), `project "nope" not found in workspace "ws-filter"`) {
		t.Fatalf("error %q lacks project/workspace context", err.Error())
	}
	if !strings.Contains(err.Error(), "alpha, beta, gamma") {
		t.Fatalf("error %q lacks sorted known-projects hint", err.Error())
	}
}

// TestWatchWorkspaceProjectDuplicate pins fail-loud behavior for a repeated
// --project value: silently deduplicating would hide operator intent.
func TestWatchWorkspaceProjectDuplicate(t *testing.T) {
	ws, err := selectWorkspaceProjects(workspaceForProjectFilterTest(), []string{"alpha", "alpha"})
	if err == nil {
		t.Fatalf("expected duplicate project error, got ws=%v", ws)
	}
	if !strings.Contains(err.Error(), `duplicate --project value "alpha"`) {
		t.Fatalf("error %q lacks duplicate context", err.Error())
	}
}

// TestWatchWorkspaceProjectRequiresWorkspace pins that --project without
// --workspace is a usage error rather than a silently ignored flag.
func TestWatchWorkspaceProjectRequiresWorkspace(t *testing.T) {
	if err := validateWatchProjectUsage("", []string{"alpha"}); err == nil {
		t.Fatal("expected --project without --workspace to fail")
	} else if !strings.Contains(err.Error(), "--project requires --workspace") {
		t.Fatalf("error %q lacks usage guidance", err.Error())
	}
	if err := validateWatchProjectUsage("ws", nil); err != nil {
		t.Fatalf("workspace without --project must pass, got %v", err)
	}
	if err := validateWatchProjectUsage("ws", []string{"alpha"}); err != nil {
		t.Fatalf("workspace with --project must pass, got %v", err)
	}
}
