package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/indexer"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
)

// Convergence tests added by the SEE-1597 P1 hardener: each test kills a
// specific surviving mutant found by isolated manual mutation testing on
// shared/SEE-1597@4fdd19a. See see1597-p1-hardener.json on the issue.

// okSaveSymbolStore succeeds at every stage so a strict scan-stage failure
// can be isolated from the save/persist stages.
type okSaveSymbolStore struct {
	failingSaveSymbolStore
}

func (o *okSaveSymbolStore) SaveFileWithSignature(ctx context.Context, filePath string, contentHash, extractorVersion string, symbols []trace.Symbol, refs []trace.Reference) error {
	return nil
}

// failingListStore fails ListDocuments from the second call onward: the
// initial indexer scan reads the existing document list exactly once
// (indexer.go), so the RPG stage's BuildFull call is the one that fails. A
// selected project whose RPG graph cannot be built must abort startup
// instead of continuing on a stale/empty graph.
type failingListStore struct {
	mockVectorStore
	listErr  error
	listRefs int
}

func (f *failingListStore) ListDocuments(_ context.Context) ([]string, error) {
	f.listRefs++
	if f.listRefs >= 2 {
		return nil, f.listErr
	}
	return nil, nil
}

// TestRuntimeInitBuildFullFailAborts pins the rpg-stage fail-loud branch for
// RPGEncoder.BuildFull (kills mutant F8-buildfull-warn).
func TestRuntimeInitBuildFullFailAborts(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc buildFailFn() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	projCfg := config.DefaultConfig()
	projCfg.RPG.Enabled = true
	if err := projCfg.Save(proj); err != nil {
		t.Fatalf("save cfg: %v", err)
	}
	ws := runtimeTestWorkspace(base, []config.ProjectEntry{{Name: "bp", Path: proj}})
	shared := &failingListStore{listErr: errors.New("injected list failure")}

	_, _, err := initializeWorkspaceRuntimes(context.Background(), ws, &noOpEmbedder{}, shared, false)
	if err == nil {
		t.Fatal("expected runtime init to fail when RPG BuildFull fails")
	}
	if !strings.Contains(err.Error(), "failed to build RPG graph") {
		t.Fatalf("err %q lacks rpg-stage context", err.Error())
	}
	if !strings.Contains(err.Error(), "injected list failure") {
		t.Fatalf("err %q lacks injected reason", err.Error())
	}
}

// TestWorkspaceWatchStrictScanStageScanFileFail pins the scan-stage strict
// branch: a file the walker lists but ScanFile cannot read must be collected
// as a strict failure and abort startup (kills mutant F11-strict-scan-ignored).
func TestWorkspaceWatchStrictScanStageScanFileFail(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := filepath.Join(proj, "broken.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	// The directory walker lists the file (lstat needs no read permission)
	// but ScanFile's content read fails with EACCES.
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o644) })

	ignoreMatcher, err := indexer.NewIgnoreMatcher(proj, []string{}, "")
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}
	scanner := indexer.NewScanner(proj, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(proj, "index.gob"))
	idx := indexer.NewIndexer(proj, vecStore, &noOpEmbedder{}, chunker, scanner, time.Now().Add(time.Hour))
	extractor := trace.NewRegexExtractor()

	_, err = runInitialScan(context.Background(), idx, scanner, extractor, &okSaveSymbolStore{}, []string{".go"}, time.Time{}, true, nil, nil, true)
	if err == nil {
		t.Fatal("expected strict initial scan to fail on unreadable file at the scan stage")
	}
	if !strings.Contains(err.Error(), "symbol stage failed for 1 file(s)") {
		t.Fatalf("err %q lacks strict summary", err.Error())
	}
	if !strings.Contains(err.Error(), "scan ") || !strings.Contains(err.Error(), "broken.go") {
		t.Fatalf("err %q lacks scan-stage entry for broken.go", err.Error())
	}
}

// TestWorkspaceStoreProbeUnsupportedBackendFails pins that the availability
// probe fails loud on a non-postgres (or DSN-less) workspace store instead of
// reporting a fake servable state (kills mutant S5-probe-noerror-gob).
func TestWorkspaceStoreProbeUnsupportedBackendFails(t *testing.T) {
	ws := &config.Workspace{
		Name:  "pb-ws",
		Store: config.StoreConfig{Backend: "gob"},
	}
	proj := config.ProjectEntry{Name: "p", Path: t.TempDir()}
	found, err := workspaceStoreProbe(context.Background(), ws, proj)
	if err == nil {
		t.Fatalf("probe on backend %q must fail loud, got found=%v", ws.Store.Backend, found)
	}
	if !strings.Contains(err.Error(), "availability probe unsupported") {
		t.Fatalf("err %q lacks unsupported-backend reason", err.Error())
	}
}

// TestShowWorkspaceStatusLocalLineSuffixPinned pins the rendered
// locally-available line including its check-mark suffix (kills mutant
// S8n-local-suffix-drop from the a2cb0ab differential matrix).
func TestShowWorkspaceStatusLocalLineSuffixPinned(t *testing.T) {
	base := t.TempDir()
	local := filepath.Join(base, "local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ws := &config.Workspace{
		Name:  "sfx-ws",
		Store: config.StoreConfig{Backend: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://probe/sfx"}},
		Projects: []config.ProjectEntry{
			{Name: "local", Path: local},
		},
	}
	oldProbe := workspaceStoreProbe
	t.Cleanup(func() { workspaceStoreProbe = oldProbe })
	workspaceStoreProbe = func(ctx context.Context, ws *config.Workspace, project config.ProjectEntry) (bool, error) {
		return false, fmt.Errorf("db down")
	}

	out := captureStdout(t, func() {
		if err := showWorkspaceStatus(ws); err != nil {
			t.Fatalf("showWorkspaceStatus: %v", err)
		}
	})
	localLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "    - local:") {
			localLine = line
		}
	}
	if localLine == "" {
		t.Fatalf("local project line missing from output %q", out)
	}
	if !strings.Contains(localLine, "locally-available ✓") {
		t.Fatalf("locally-available line %q must render the check-mark suffix", localLine)
	}
}
