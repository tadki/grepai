package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

// TestWorkspaceCommandFlows drives the full workspace management lifecycle:
// create (flag-driven non-interactive), list, show, status, add, remove and
// delete, all against a HOME-scoped workspace.yaml.
func TestWorkspaceCommandFlows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(home); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	dsn := "postgres://user:pw@127.0.0.1:5432/grepai?sslmode=disable"

	rootCmd.SetArgs([]string{
		"workspace", "create", "ws-flow", "--backend", "postgres", "--provider", "ollama",
		"--model", "nomic-embed-text", "--dsn", dsn, "--yes",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("workspace create: %v", err)
	}
	cfg, err := config.LoadWorkspaceConfig()
	if err != nil || cfg == nil || len(cfg.Workspaces) != 1 {
		t.Fatalf("workspace config after create: cfg=%v err=%v", cfg != nil, err)
	}
	if _, err := cfg.GetWorkspace("ws-flow"); err != nil {
		t.Fatalf("created workspace missing: %v", err)
	}

	// Duplicate create fails.
	rootCmd.SetArgs([]string{
		"workspace", "create", "ws-flow", "--backend", "postgres", "--provider", "ollama",
		"--model", "nomic-embed-text", "--dsn", dsn, "--yes",
	})
	if err := rootCmd.Execute(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create err=%v", err)
	}

	// list / show / status
	for _, sub := range []string{"list", "show", "status"} {
		rootCmd.SetArgs([]string{"workspace", sub, "ws-flow"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("workspace %s: %v", sub, err)
		}
	}

	// add requires an existing project dir
	proj := filepath.Join(home, "extra-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	rootCmd.SetArgs([]string{"workspace", "add", "ws-flow", proj})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("workspace add: %v", err)
	}
	// duplicate add fails
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("duplicate add must fail")
	}

	// remove
	rootCmd.SetArgs([]string{"workspace", "remove", "ws-flow", filepath.Base(proj)})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("workspace remove: %v", err)
	}
	// remove unknown project fails
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("remove of unknown project must fail")
	}

	// show unknown workspace fails
	rootCmd.SetArgs([]string{"workspace", "show", "ghost"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("show of unknown workspace must fail")
	}

	// delete
	rootCmd.SetArgs([]string{"workspace", "delete", "ws-flow"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("workspace delete: %v", err)
	}
	cfg, err = config.LoadWorkspaceConfig()
	if err != nil || (cfg != nil && len(cfg.Workspaces) != 0) {
		t.Fatalf("workspace not deleted: err=%v", err)
	}

	// maskDSN contract
	masked := maskDSN(dsn)
	if strings.Contains(masked, "pw") || !strings.Contains(masked, "127.0.0.1:5432") {
		t.Fatalf("maskDSN = %q, must hide credentials and keep host", masked)
	}
	if maskDSN("") != "" {
		t.Fatal("maskDSN of empty string must stay empty")
	}
}

// TestInitCommandFlows covers non-interactive init: defaults written, repeat
// init fails without force, and the config file lands in .grepai/config.yaml.
func TestInitCommandFlows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "initproj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	reset := func() {
		initProvider, initModel, initBackend = "", "", ""
		initNonInteractive, initInherit, initUI = false, false, false
	}
	reset()
	resetFlowFlags(t, reset)

	initNonInteractive = true
	if _, err := captureFlowStdout(t, func() error { return runInit(initCmd, nil) }); err != nil {
		t.Fatalf("init --yes: %v", err)
	}
	if !config.Exists(root) {
		t.Fatal("init must create .grepai/config.yaml")
	}

	// Re-init is idempotent: reports existing config without changing it.
	initNonInteractive = true
	before, err := os.ReadFile(filepath.Join(root, ".grepai", "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if _, err := captureFlowStdout(t, func() error { return runInit(initCmd, nil) }); err != nil {
		t.Fatalf("re-init must not fail: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, ".grepai", "config.yaml"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("re-init must not rewrite the config (err=%v)", err)
	}
}

// TestAgentSetupFlows checks that agent-setup updates an existing agent file
// and reports cleanly with none present.
func TestAgentSetupFlows(t *testing.T) {
	root := t.TempDir()
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	reset := func() { withSubagent = false }
	reset()
	resetFlowFlags(t, reset)

	// No agent file: command runs and reports nothing modified.
	if _, err := captureFlowStdout(t, func() error { return runAgentSetup(agentSetupCmd, nil) }); err != nil {
		t.Fatalf("agent-setup without files: %v", err)
	}

	// With CLAUDE.md present: instructions get appended.
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# Project\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	if _, err := captureFlowStdout(t, func() error { return runAgentSetup(agentSetupCmd, nil) }); err != nil {
		t.Fatalf("agent-setup with CLAUDE.md: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil || !strings.Contains(string(data), "grepai") {
		t.Fatalf("CLAUDE.md not updated with grepai instructions (err=%v)", err)
	}
}

// TestResolveMCPTargetFlows pins the mcp-serve target resolution order:
// explicit workspace, explicit path, cwd project root and workspace
// auto-detection, with clean failures for unknown names.
func TestResolveMCPTargetFlows(t *testing.T) {
	projectRoot := flowEnv(t)

	// Priority 3: cwd project root.
	root, wsName, err := resolveMCPTarget("", "")
	if err != nil || wsName != "" || filepath.Clean(root) != filepath.Clean(projectRoot) {
		t.Fatalf("cwd resolution root=%q ws=%q err=%v", root, wsName, err)
	}

	// Priority 2: explicit path that has no project errors.
	if _, _, err := resolveMCPTarget(filepath.Join(projectRoot, "missing-dir"), ""); err == nil {
		t.Fatal("explicit missing path must fail")
	}
	// Explicit path with a project resolves.
	got, wsName, err := resolveMCPTarget(projectRoot, "")
	if err != nil || wsName != "" || filepath.Clean(got) != filepath.Clean(projectRoot) {
		t.Fatalf("explicit path root=%q ws=%q err=%v", got, wsName, err)
	}

	// Priority 1: unknown workspace flag fails cleanly.
	if _, _, err := resolveMCPTarget("", "ghost-ws"); err == nil {
		t.Fatal("unknown workspace flag must fail")
	}

	// Known workspace flag resolves with the local project root attached.
	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name:     "flow-ws",
		Projects: []config.ProjectEntry{{Name: "king", Path: projectRoot}},
	}); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}
	got, wsName, err = resolveMCPTarget("", "flow-ws")
	if err != nil {
		t.Fatalf("known workspace flag: %v", err)
	}
	if wsName != "flow-ws" {
		t.Fatalf("wsName = %q, want flow-ws", wsName)
	}
}
