package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

// TestWorkspaceWatchSelectedPathFail pins the path-stage contract: every
// stat error on a selected project fails with project, path and reason.
// IsNotExist is the common case; the non-IsNotExist counterexample (a path
// behind a non-searchable parent, i.e. EACCES) proves the old
// os.IsNotExist-only guard could let stat failures through silently.
func TestWorkspaceWatchSelectedPathFail(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based counterexample is ineffective for root")
	}
	base := t.TempDir()
	existing := filepath.Join(base, "ok")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lockedParent := filepath.Join(base, "locked")
	if err := os.MkdirAll(lockedParent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	unreadable := filepath.Join(lockedParent, "child")
	// Make the parent non-searchable so stat on the child fails with EACCES,
	// which is NOT os.IsNotExist.
	if err := os.Chmod(lockedParent, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedParent, 0o755) })

	if _, err := os.Stat(unreadable); err == nil || os.IsNotExist(err) {
		t.Fatalf("test setup: expected a non-IsNotExist stat error for %s, got err=%v", unreadable, err)
	}

	ws := &config.Workspace{
		Name: "ws-path",
		Projects: []config.ProjectEntry{
			{Name: "gone", Path: filepath.Join(base, "missing")},
			{Name: "ok", Path: existing},
		},
	}

	err := validateWorkspaceProjectPaths(ws)
	if err == nil {
		t.Fatal("expected path stage to fail on stat errors")
	}
	if !strings.Contains(err.Error(), `project "gone"`) || !strings.Contains(err.Error(), `path stage`) {
		t.Fatalf("error %q lacks project/path-stage context", err.Error())
	}

	// Non-IsNotExist counterexample: stat fails with a permission error and
	// the path stage must still fail loud, naming the project.
	locked := &config.Workspace{
		Name:     "ws-locked",
		Projects: []config.ProjectEntry{{Name: "locked", Path: unreadable}},
	}
	err = validateWorkspaceProjectPaths(locked)
	if err == nil {
		t.Fatal("expected path stage to fail on the EACCES stat error (non-IsNotExist)")
	}
	if !strings.Contains(err.Error(), `project "locked"`) || !strings.Contains(err.Error(), "path stage") {
		t.Fatalf("error %q lacks locked-project context", err.Error())
	}
	if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
		t.Fatalf("counterexample must be a non-IsNotExist failure, got %s", err.Error())
	}

	healthy := &config.Workspace{Name: "ws-ok", Projects: []config.ProjectEntry{{Name: "ok", Path: existing}}}
	if err := validateWorkspaceProjectPaths(healthy); err != nil {
		t.Fatalf("healthy workspace must pass: %v", err)
	}
}
