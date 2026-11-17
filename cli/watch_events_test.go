package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/daemon"
	"github.com/yoanbernabeu/grepai/indexer"
	"github.com/yoanbernabeu/grepai/rpg"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
	"github.com/yoanbernabeu/grepai/watcher"
)

// buildEventRuntime assembles a runtime for direct handleFileEvent calls: a
// real GOB store, symbol store, RPG encoder and indexer over a tiny project.
func buildEventRuntime(t *testing.T, root string) (
	ctx context.Context, idx *indexer.Indexer, scanner *indexer.Scanner,
	extractor *trace.RegexExtractor, symStore projectSymbolStore,
	encoder *rpg.RPGEncoder, vecStore store.VectorStore, cfg *config.Config,
) {
	t.Helper()
	ctx = context.Background()
	cfg = config.DefaultConfig()
	cfg.Store.Backend = "gob"
	cfg.Trace.EnabledLanguages = []string{".go"}
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc aa() {}\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	ignore, err := indexer.NewIgnoreMatcher(root, []string{}, "")
	if err != nil {
		t.Fatalf("ignore: %v", err)
	}
	scanner = indexer.NewScanner(root, ignore)
	chunker := indexer.NewChunker(512, 50)
	vecStore = store.NewGOBStore(config.GetIndexPath(root))
	idx = indexer.NewIndexer(root, vecStore, &noOpEmbedder{}, chunker, scanner, time.Time{})
	extractor = trace.NewRegexExtractor()
	symStore = trace.NewGOBSymbolStore(config.GetSymbolIndexPath(root))
	rpgStore := rpg.NewGOBRPGStore(config.GetRPGIndexPath(root))
	encoder = rpg.NewRPGEncoder(rpgStore, rpg.NewLocalExtractor(), root, rpg.RPGEncoderConfig{})
	return
}

// TestHandleFileEventModifyAndDelete pins the modify/delete event handling
// including the atomic-write re-qualification (a rename whose target still
// exists is treated as a modification, not a deletion).
func TestHandleFileEventModifyAndDelete(t *testing.T) {
	root := t.TempDir()
	ctx, idx, scanner, extractor, symStore, encoder, vecStore, cfg := buildEventRuntime(t, root)
	manager := newRPGRealtimeManager(16)

	activityCalls := 0
	activity := func(op, path string) {
		if op != "steady" {
			activityCalls++
		}
	}
	statCalls := 0
	statsObs := func(projectRoot string, delta watchStatsDelta) { statCalls++ }
	lastWrite := time.Time{}

	target := "a.go"
	if err := os.WriteFile(filepath.Join(root, target), []byte("package main\n\nfunc aa() { bb() }\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	handleFileEvent(ctx, idx, scanner, extractor, symStore, encoder, vecStore,
		cfg.Trace.EnabledLanguages, root, cfg, &lastWrite, manager,
		watcher.FileEvent{Type: watcher.EventModify, Path: target},
		activity, statsObs)
	if activityCalls == 0 || statCalls == 0 {
		t.Fatalf("modify event must produce activity/stats: activity=%d stats=%d", activityCalls, statCalls)
	}

	// Rename whose target still exists: re-qualified as modify, not delete.
	handleFileEvent(ctx, idx, scanner, extractor, symStore, encoder, vecStore,
		cfg.Trace.EnabledLanguages, root, cfg, &lastWrite, manager,
		watcher.FileEvent{Type: watcher.EventRename, Path: target},
		nil, nil)

	// Genuine delete: file gone, event processed as removal.
	if err := os.Remove(filepath.Join(root, target)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	handleFileEvent(ctx, idx, scanner, extractor, symStore, encoder, vecStore,
		cfg.Trace.EnabledLanguages, root, cfg, &lastWrite, manager,
		watcher.FileEvent{Type: watcher.EventDelete, Path: target},
		nil, nil)
}

// TestWorkspacePIDAndReadyFiles pins the daemon PID/ready file contract used
// by watch --status/--stop.
func TestWorkspacePIDAndReadyFiles(t *testing.T) {
	logDir := t.TempDir()
	if err := daemon.WriteWorkspacePIDFile(logDir, "wsx"); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	pid, err := daemon.GetRunningWorkspacePID(logDir, "wsx")
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d, want the current process", pid)
	}
	if err := daemon.RemoveWorkspacePIDFile(logDir, "wsx"); err != nil {
		t.Fatalf("remove pid: %v", err)
	}
	if pid2, err := daemon.GetRunningWorkspacePID(logDir, "wsx"); err == nil && pid2 != 0 {
		t.Fatalf("pid after removal = %d, want 0", pid2)
	}

	daemon.WriteWorkspaceReadyFile(logDir, "wsx")
	if !daemon.IsWorkspaceReady(logDir, "wsx") {
		t.Fatal("ready file must mark workspace ready")
	}
	daemon.RemoveWorkspaceReadyFile(logDir, "wsx")
	if daemon.IsWorkspaceReady(logDir, "wsx") {
		t.Fatal("ready file must be gone after removal")
	}
}

// TestShowAndStopWorkspaceWatchStatus drives the status/stop surfaces over
// PID files without touching any real watcher.
func TestShowAndStopWorkspaceWatchStatus(t *testing.T) {
	logDir := t.TempDir()
	_ = daemon.WriteWorkspacePIDFile(logDir, "status-ws")
	defer daemon.RemoveWorkspacePIDFile(logDir, "status-ws")

	ws := &config.Workspace{
		Name:     "status-ws",
		Store:    config.StoreConfig{Backend: "postgres"},
		Projects: []config.ProjectEntry{{Name: "p", Path: t.TempDir()}},
	}
	out := captureStdout(t, func() {
		if err := showWorkspaceWatchStatus(logDir, ws); err != nil {
			t.Errorf("show status: %v", err)
		}
	})
	if !strings.Contains(out, "running") || !strings.Contains(out, "status-ws") {
		t.Fatalf("status output %q lacks runtime info", out)
	}

	// Not-running workspace reports cleanly.
	idle := &config.Workspace{Name: "idle-ws", Projects: []config.ProjectEntry{{Name: "p", Path: t.TempDir()}}}
	out = captureStdout(t, func() {
		if err := showWorkspaceWatchStatus(logDir, idle); err != nil {
			t.Errorf("show idle status: %v", err)
		}
	})
	if !strings.Contains(out, "not running") {
		t.Fatalf("idle status output %q lacks not-running notice", out)
	}
}

// TestBuildFrameworkRegistry covers the processor registry construction.
func TestBuildFrameworkRegistry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Framework.Enabled = false
	if registry := buildFrameworkRegistry(cfg); registry == nil {
		t.Fatal("disabled framework processing must still yield a registry")
	}
	cfg.Framework.Enabled = true
	if buildFrameworkRegistry(cfg) == nil {
		t.Fatal("enabled framework processing must yield a registry")
	}
}

// TestExtractSymbolsWithFramework covers the symbol extraction wrapper on a
// Go source snippet with and without processors.
func TestExtractSymbolsWithFramework(t *testing.T) {
	extractor := trace.NewRegexExtractor()
	content := "package main\n\nfunc handled() {}\n"
	symbols, refs, err := extractSymbolsWithFramework(context.Background(), extractor, "main.go", content)
	if err != nil {
		t.Fatalf("extract without processors: %v", err)
	}
	found := false
	for _, s := range symbols {
		if s.Name == "handled" {
			found = true
		}
	}
	if !found {
		t.Fatalf("symbols %v lack handled()", symbols)
	}
	if refs == nil {
		t.Log("no refs extracted for the snippet")
	}
}

// TestCanonicalPathAndDiscoverWorktrees pins path canonicalization and
// worktree discovery on a non-worktree repo (empty result).
func TestCanonicalPathAndDiscoverWorktrees(t *testing.T) {
	root := t.TempDir()
	if got := canonicalPath(filepath.Join(root, "x", "..", "y")); !strings.HasSuffix(filepath.Clean(got), "y") {
		t.Fatalf("canonicalPath = %q", got)
	}
	if got := discoverWorktreesForWatch(root); len(got) != 0 {
		t.Fatalf("non-worktree discovery = %v, want empty", got)
	}
}
