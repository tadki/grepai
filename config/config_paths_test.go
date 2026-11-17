package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestIndexPathHelpers pins the per-project index file layout.
func TestIndexPathHelpers(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, ".grepai")
	if got := GetIndexPath(root); got != filepath.Join(want, IndexFileName) {
		t.Fatalf("GetIndexPath = %q", got)
	}
	if got := GetSymbolIndexPath(root); got != filepath.Join(want, SymbolIndexFileName) {
		t.Fatalf("GetSymbolIndexPath = %q", got)
	}
	if got := GetRPGIndexPath(root); got != filepath.Join(want, RPGIndexFileName) {
		t.Fatalf("GetRPGIndexPath = %q", got)
	}
}

// TestAutoInitWorktree pins the zero-config worktree seeding: config.yaml is
// required, missing main config aborts and cleans up, index/symbol seeds are
// copied when present.
func TestAutoInitWorktree(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt")
	for _, d := range []string{main, wt} {
		if err := os.MkdirAll(filepath.Join(d, ".grepai"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	// Missing main config: must abort and clean the partial .grepai dir.
	if err := AutoInitWorktree(wt, main); err == nil {
		t.Fatal("expected error when main worktree lacks config.yaml")
	}
	if _, err := os.Stat(filepath.Join(wt, ".grepai")); !os.IsNotExist(err) {
		t.Fatal("partial .grepai must be removed after failed auto-init")
	}

	// Full seed: config + index + symbols copied.
	cfg := DefaultConfig()
	if err := cfg.Save(main); err != nil {
		t.Fatalf("save main config: %v", err)
	}
	for _, name := range []string{"index.gob", "symbols.gob"} {
		if err := os.WriteFile(filepath.Join(main, ".grepai", name), []byte("seed-"+name), 0o644); err != nil {
			t.Fatalf("write seed %s: %v", name, err)
		}
	}
	if err := AutoInitWorktree(wt, main); err != nil {
		t.Fatalf("AutoInitWorktree: %v", err)
	}
	for _, name := range []string{"config.yaml", "index.gob", "symbols.gob"} {
		data, err := os.ReadFile(filepath.Join(wt, ".grepai", name))
		if err != nil || len(data) == 0 {
			t.Fatalf("seeded %s missing or empty (err=%v)", name, err)
		}
	}
}

// TestFindProjectRootWithGit covers git-repo resolution: the worktree root is
// found via the local .grepai dir, and a repo without any config surfaces the
// not-found error.
func TestFindProjectRootWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = base
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", ".")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// No config anywhere in the repo: expect an error.
	if err := os.Chdir(base); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if root, _, err := FindProjectRootWithGit(); err == nil {
		t.Fatalf("expected not-found error in bare repo, got root=%q", root)
	}

	// With .grepai/config.yaml at the repo root: root resolves to the repo.
	cfg := DefaultConfig()
	if err := cfg.Save(base); err != nil {
		t.Fatalf("save config: %v", err)
	}
	root, info, err := FindProjectRootWithGit()
	if err != nil {
		t.Fatalf("FindProjectRootWithGit: %v", err)
	}
	if filepath.Clean(root) != filepath.Clean(base) {
		t.Fatalf("root = %q, want %q", root, base)
	}
	if info == nil {
		t.Fatal("expected git detect info in a git repo")
	}
}
