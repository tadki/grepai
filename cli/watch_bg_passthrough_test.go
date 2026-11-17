package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

// TestMain intercepts the spawned background child: spawnBackgroundWithLog
// re-executes os.Executable(), which under go test is this package's test
// binary. In child mode we dump argv to a file and exit before any test runs,
// so the suite does not recurse.
func TestMain(m *testing.M) {
	if os.Getenv("SEE1597_BG_CHILD") == "1" {
		args := strings.Join(os.Args[1:], "\x00")
		_ = os.WriteFile(os.Getenv("SEE1597_BG_ARGS_OUT"), []byte(args), 0o644)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestStartBackgroundWorkspaceWatchProjectPassthrough pins that the
// background workspace watcher forwards the --project selection to the child
// process: without the forwarding loop a filtered background watcher would
// silently widen to all projects. The child is this test binary re-executed
// by spawnBackgroundWithLog; it records argv via the TestMain guard above.
func TestStartBackgroundWorkspaceWatchProjectPassthrough(t *testing.T) {
	if os.Getenv("SEE1597_BG_CHILD") == "1" {
		t.Skip("child process guard: handled by TestMain")
	}
	resetWatchGlobals(t)
	watchWorkspace = "pt-ws"
	watchProjects = []string{"alpha", "beta"}

	childOut := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("SEE1597_BG_CHILD", "1")
	t.Setenv("SEE1597_BG_ARGS_OUT", childOut)

	ws := &config.Workspace{Name: "pt-ws"}
	logDir := t.TempDir()
	err := startBackgroundWorkspaceWatch(logDir, ws)
	// The child exits immediately without writing the ready marker, so the
	// parent reports a startup failure; the argv file is the assertion target.
	if err == nil {
		t.Fatalf("expected startup failure for immediate-exit child, got nil")
	}

	raw, rerr := os.ReadFile(childOut)
	if rerr != nil {
		t.Fatalf("child argv file missing (TestMain capture did not run?): %v; parent err=%v", rerr, err)
	}
	got := strings.Split(string(raw), "\x00")
	want := []string{"watch", "--workspace", "pt-ws", "--project", "alpha", "--project", "beta"}
	if len(got) != len(want) {
		t.Fatalf("child argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("child argv[%d] = %q, want %q (full argv %q)", i, got[i], want[i], got)
		}
	}
}
