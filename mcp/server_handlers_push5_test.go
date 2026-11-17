package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

// TestHandleListWorkspacesSortsMultiple covers the multi-workspace sort path.
func TestHandleListWorkspacesSortsMultiple(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name:     "zzz-ws",
		Projects: []config.ProjectEntry{{Name: "p", Path: filepath.Join(home, "zzz")}},
	})
	res, err := s.handleListWorkspaces(ctx, toolReq("grepai_list_workspaces", map[string]any{"format": "toon"}))
	if err != nil || res.IsError {
		t.Fatalf("multi-workspace list: err=%v", err)
	}
	if !strings.Contains(resultText(t, res), "zzz-ws") {
		t.Fatalf("list text %s lacks second workspace", resultText(t, res))
	}
}

// TestHandleIndexStatusMissingSymbolsProject covers the not-ready project
// branch of the workspace index status.
func TestHandleIndexStatusMissingSymbolsProject(t *testing.T) {
	s := handlerEnv(t)
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("homedir: %v", err)
	}
	empty := filepath.Join(home, "no-symbols-proj")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	saveFlowWorkspaceConfig(t, config.Workspace{
		Name:     "gap-ws",
		Projects: []config.ProjectEntry{{Name: "gap", Path: empty}},
	})
	res, err := s.handleIndexStatus(ctx, toolReq("grepai_index_status", map[string]any{"workspace": "gap-ws"}))
	if err != nil || res.IsError {
		t.Fatalf("gap-ws status: err=%v text=%s", err, resultText(t, res))
	}
	text := resultText(t, res)
	if !strings.Contains(text, "symbols_ready") {
		t.Fatalf("status text %s lacks symbols_ready field", text)
	}
	if !strings.Contains(text, `"symbols_ready": false`) && !strings.Contains(text, "\"symbols_ready\":false") {
		t.Fatalf("project without symbols must report not ready: %s", text)
	}
}

// TestHandleListProjectsWithCorruptConfig covers the config load error
// branch of the listing handlers.
func TestHandleListProjectsWithCorruptConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grepai", "workspace.yaml"), []byte("workspaces: [broken"), 0o644); err != nil {
		t.Fatalf("write corrupt config: %v", err)
	}
	s := &Server{}
	res, err := s.handleListProjects(context.Background(), toolReq("grepai_list_projects", map[string]any{"workspace": "any"}))
	expectToolError(t, res, err, "corrupt workspace config")
	res, err = s.handleIndexStatus(context.Background(), toolReq("grepai_index_status", map[string]any{"workspace": "any"}))
	expectToolError(t, res, err, "corrupt workspace config status")
}
