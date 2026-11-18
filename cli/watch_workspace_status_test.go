package cli

import (
	"context"
	"fmt"
	"io"
	"log"
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

// stubProbeStore is a minimal symbol store whose Close/Persist/GetStats
// behavior is scriptable for store-lifecycle and strict-scan branch tests.
type stubProbeStore struct {
	projectSymbolStore
	closeErr   error
	persistErr error
	statsErr   error
	stats      *trace.SymbolStats
}

func (s *stubProbeStore) Load(ctx context.Context) error     { return nil }
func (s *stubProbeStore) Persist(ctx context.Context) error  { return s.persistErr }
func (s *stubProbeStore) Close() error                       { return s.closeErr }
func (s *stubProbeStore) IsFileIndexed(filePath string) bool { return false }
func (s *stubProbeStore) GetFileExtractorVersion(filePath string) (string, bool) {
	return "", false
}
func (s *stubProbeStore) GetFileContentHash(filePath string) (string, bool) {
	return "", false
}
func (s *stubProbeStore) SaveFileWithSignature(ctx context.Context, filePath string, contentHash, extractorVersion string, symbols []trace.Symbol, refs []trace.Reference) error {
	return nil
}
func (s *stubProbeStore) GetStats(ctx context.Context) (*trace.SymbolStats, error) {
	if s.statsErr != nil {
		return nil, s.statsErr
	}
	return s.stats, nil
}

// captureStdout redirects os.Stdout for the duration of fn.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		done <- string(out)
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func captureCliLog(t *testing.T) func() string {
	t.Helper()
	var buf strings.Builder
	oldWriter := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldWriter) })
	return func() string { return buf.String() }
}

// TestShowWorkspaceStatusDisclosesAvailability pins that workspace status
// prints the three availability states with reasons, driven by the
// replaceable store probe.
func TestShowWorkspaceStatusDisclosesAvailability(t *testing.T) {
	base := t.TempDir()
	local := filepath.Join(base, "local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ws := &config.Workspace{
		Name:  "ws-status",
		Store: config.StoreConfig{Backend: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://probe/test"}},
		Projects: []config.ProjectEntry{
			{Name: "local", Path: local},
			{Name: "remote", Path: filepath.Join(base, "remote")},
			{Name: "broken", Path: filepath.Join(base, "broken")},
		},
	}
	oldProbe := workspaceStoreProbe
	t.Cleanup(func() { workspaceStoreProbe = oldProbe })
	workspaceStoreProbe = func(ctx context.Context, ws *config.Workspace, project config.ProjectEntry) (bool, error) {
		switch project.Name {
		case "remote":
			return true, nil
		case "broken":
			return false, fmt.Errorf("db down")
		default:
			return false, nil
		}
	}

	out := captureStdout(t, func() {
		if err := showWorkspaceStatus(ws); err != nil {
			t.Fatalf("showWorkspaceStatus: %v", err)
		}
	})
	for _, want := range []string{
		"local: ", "locally-available",
		"remote: ", "serving-only", "served from shared store",
		"broken: ", "failed", "db down",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output %q lacks %q", out, want)
		}
	}
}

// TestCloseWorkspaceRuntimeStoresWarnsButContinues pins that shutdown-time
// store Close failures warn without panicking, and nil stores are a no-op.
func TestCloseWorkspaceRuntimeStoresWarnsButContinues(t *testing.T) {
	getLog := captureCliLog(t)
	rt := &workspaceProjectRuntime{
		project:     config.ProjectEntry{Name: "p", Path: "/x"},
		symbolStore: &stubProbeStore{closeErr: fmt.Errorf("boom")},
	}
	closeWorkspaceRuntimeStores(rt)
	if !strings.Contains(getLog(), "failed to close symbol store") {
		t.Fatal("expected a warning for the failing symbol store close")
	}
	closeWorkspaceRuntimeStores(&workspaceProjectRuntime{})
}

// TestWorkspaceWatchSelectedStrictScanPersistFail pins strict-mode abort when
// the symbol index cannot be persisted.
func TestWorkspaceWatchSelectedStrictScanPersistFail(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc real() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ignoreMatcher, err := indexer.NewIgnoreMatcher(proj, []string{}, "", nil)
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}
	scanner := indexer.NewScanner(proj, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(proj, "index.gob"))
	idx := indexer.NewIndexer(proj, vecStore, &noOpEmbedder{}, chunker, scanner, time.Time{})
	extractor := trace.NewRegexExtractor()

	st := &stubProbeStore{persistErr: fmt.Errorf("injected persist failure"), stats: &trace.SymbolStats{}}
	if _, err := runInitialScan(context.Background(), idx, scanner, extractor, st, []string{".go"}, time.Time{}, true, nil, nil, true); err == nil {
		t.Fatal("expected strict scan to fail on persist error")
	} else if !strings.Contains(err.Error(), "symbol persist failed") {
		t.Fatalf("error %q lacks persist stage", err.Error())
	}
	// Non-strict mode keeps the historical warn+continue behavior.
	getLog := captureCliLog(t)
	if _, err := runInitialScan(context.Background(), idx, scanner, extractor, st, []string{".go"}, time.Time{}, true, nil, nil, false); err != nil {
		t.Fatalf("non-strict scan must not fail: %v", err)
	}
	if !strings.Contains(getLog(), "failed to persist symbol index") {
		t.Fatal("non-strict mode must warn on persist failure")
	}
}

// TestWorkspaceWatchSelectedStrictScanStatsFail pins strict-mode abort when
// the store cannot report its totals (the delta-vs-total log contract).
func TestWorkspaceWatchSelectedStrictScanStatsFail(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc real() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ignoreMatcher, err := indexer.NewIgnoreMatcher(proj, []string{}, "", nil)
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}
	scanner := indexer.NewScanner(proj, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(proj, "index.gob"))
	idx := indexer.NewIndexer(proj, vecStore, &noOpEmbedder{}, chunker, scanner, time.Time{})
	extractor := trace.NewRegexExtractor()

	st := &stubProbeStore{statsErr: fmt.Errorf("injected stats failure")}
	if _, err := runInitialScan(context.Background(), idx, scanner, extractor, st, []string{".go"}, time.Time{}, true, nil, nil, true); err == nil {
		t.Fatal("expected strict scan to fail on stats error")
	} else if !strings.Contains(err.Error(), "symbol stats failed") {
		t.Fatalf("error %q lacks stats stage", err.Error())
	}
}

// TestWorkspaceWatchStrictScanReportsTotals pins the delta+total log line for
// a successful strict run against a real GOB store.
func TestWorkspaceWatchStrictScanReportsTotals(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc real() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ignoreMatcher, err := indexer.NewIgnoreMatcher(proj, []string{}, "", nil)
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}
	scanner := indexer.NewScanner(proj, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(proj, "index.gob"))
	idx := indexer.NewIndexer(proj, vecStore, &noOpEmbedder{}, chunker, scanner, time.Time{})
	extractor := trace.NewRegexExtractor()
	symStore := trace.NewGOBSymbolStore(filepath.Join(proj, "symbols.gob"))

	getLog := captureCliLog(t)
	stats, err := runInitialScan(context.Background(), idx, scanner, extractor, symStore, []string{".go"}, time.Time{}, true, nil, nil, true)
	if err != nil {
		t.Fatalf("strict scan on real store failed: %v", err)
	}
	if stats.FilesIndexed == 0 {
		t.Fatal("expected at least one indexed file")
	}
	logged := getLog()
	if !strings.Contains(logged, "Symbol index built:") || !strings.Contains(logged, "total in index") {
		t.Fatalf("log %q lacks delta+total contract", logged)
	}
}

// TestWorkspaceWatchSelectedRuntimeInitSuccessPath pins the green path: a
// GOB-backed workspace with an RPG-enabled project initializes a full
// runtime (scan, symbols, RPG build/persist, watcher) without error.
func TestWorkspaceWatchSelectedRuntimeInitSuccessPath(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc real() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(proj); err != nil {
		t.Fatalf("save config: %v", err)
	}

	ws := &config.Workspace{
		Name:     "ws-success",
		Store:    config.StoreConfig{Backend: "qdrant"},
		Projects: []config.ProjectEntry{{Name: "ok", Path: proj}},
	}
	shared := store.NewGOBStore(filepath.Join(proj, "shared-index.gob"))

	runtimes, watchers, err := initializeWorkspaceRuntimes(context.Background(), ws, &noOpEmbedder{}, shared, true)
	if err != nil {
		t.Fatalf("success path failed: %v", err)
	}
	if len(runtimes) != 1 || len(watchers) != 1 {
		t.Fatalf("runtimes=%d watchers=%d, want 1/1", len(runtimes), len(watchers))
	}
	runtime := runtimes[filepath.Clean(proj)]
	if runtime == nil {
		t.Fatal("runtime missing for initialized project")
	}
	if runtime.rpgStore == nil {
		t.Fatal("RPG-enabled project must end with an RPG store")
	}
	for _, w := range watchers {
		w.Close()
	}
	for _, rt := range runtimes {
		closeWorkspaceRuntimeStores(rt)
	}
}

// TestWorkspaceWatchStrictScanCapsFailureList pins the aggregated failure
// summary: six failing files produce a six-file count with five shown.
func TestWorkspaceWatchStrictScanCapsFailureList(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("file%d.go", i)
		if err := os.WriteFile(filepath.Join(proj, name), []byte("package main\n\nfunc f"+fmt.Sprint(i)+"() {}\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	ignoreMatcher, err := indexer.NewIgnoreMatcher(proj, []string{}, "", nil)
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}
	scanner := indexer.NewScanner(proj, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(proj, "index.gob"))
	idx := indexer.NewIndexer(proj, vecStore, &noOpEmbedder{}, chunker, scanner, time.Time{})
	extractor := trace.NewRegexExtractor()

	st := &failingSaveSymbolStore{}
	_, err = runInitialScan(context.Background(), idx, scanner, extractor, st, []string{".go"}, time.Time{}, true, nil, nil, true)
	if err == nil {
		t.Fatal("expected strict scan to fail on six injected failures")
	}
	if !strings.Contains(err.Error(), "symbol stage failed for 6 file(s)") {
		t.Fatalf("error %q lacks the six-file count", err.Error())
	}
	if strings.Count(err.Error(), "file5.go") != 0 {
		t.Fatalf("error %q must cap displayed paths at five", err.Error())
	}
	if !strings.Contains(err.Error(), "file4.go") {
		t.Fatalf("error %q must show the first five paths", err.Error())
	}
}

// TestWorkspaceWatchStrictScanPrintsTotalsLine pins the non-background UI
// variant of the delta+total symbol line.
func TestWorkspaceWatchStrictScanPrintsTotalsLine(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc real() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ignoreMatcher, err := indexer.NewIgnoreMatcher(proj, []string{}, "", nil)
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}
	scanner := indexer.NewScanner(proj, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(proj, "index.gob"))
	idx := indexer.NewIndexer(proj, vecStore, &noOpEmbedder{}, chunker, scanner, time.Time{})
	extractor := trace.NewRegexExtractor()
	symStore := trace.NewGOBSymbolStore(filepath.Join(proj, "symbols.gob"))

	out := captureStdout(t, func() {
		if _, err := runInitialScan(context.Background(), idx, scanner, extractor, symStore, []string{".go"}, time.Time{}, false, nil, nil, true); err != nil {
			t.Errorf("strict scan failed: %v", err)
		}
	})
	if !strings.Contains(out, "Symbol index built:") || !strings.Contains(out, "total in index") {
		t.Fatalf("stdout %q lacks delta+total line", out)
	}
}

// TestWorkspaceWatchServingOnlyStatusClassification names the serving-only
// disclosure explicitly for the workspace-watch failure matrix: a project
// with no local checkout but live shared-store data is servable, not failed.
func TestWorkspaceWatchServingOnlyStatusClassification(t *testing.T) {
	state, reason := classifyWorkspaceProjectAvailability(os.ErrNotExist, true, nil)
	if state != "serving-only" || reason != "" {
		t.Fatalf("state=%q reason=%q, want serving-only with empty reason", state, reason)
	}
}

// TestShowWorkspaceStatusEACCESNotLocallyAvailable is the MEDIUM-1 regression
// from Final Review R1: a path whose stat fails with a non-ENOENT error
// (EACCES behind a non-searchable parent) must surface as failed through the
// full status output chain — never as locally-available. The assertion runs
// against the rendered stdout, not just the pure classifier.
func TestShowWorkspaceStatusEACCESNotLocallyAvailable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based counterexample is ineffective for root")
	}
	base := t.TempDir()
	local := filepath.Join(base, "local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lockedParent := filepath.Join(base, "locked")
	if err := os.MkdirAll(lockedParent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	unreadable := filepath.Join(lockedParent, "child")
	if err := os.Chmod(lockedParent, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedParent, 0o755) })

	// Setup guard: stat on the child must fail with a non-ENOENT error.
	if _, err := os.Stat(unreadable); err == nil || os.IsNotExist(err) {
		t.Fatalf("setup: expected non-ENOENT stat error for %s, got err=%v", unreadable, err)
	}

	ws := &config.Workspace{
		Name:  "eacc-ws",
		Store: config.StoreConfig{Backend: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://probe/eacc"}},
		Projects: []config.ProjectEntry{
			{Name: "local", Path: local},
			{Name: "eaccess", Path: unreadable},
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

	// The healthy project stays locally-available.
	localLine := ""
	eaccessLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "    - local:") {
			localLine = line
		}
		if strings.HasPrefix(line, "    - eaccess:") {
			eaccessLine = line
		}
	}
	if localLine == "" || !strings.Contains(localLine, "locally-available") {
		t.Fatalf("local project line %q must stay locally-available", localLine)
	}
	if eaccessLine == "" {
		t.Fatalf("eaccess project line missing from output %q", out)
	}
	if strings.Contains(eaccessLine, "locally-available") {
		t.Fatalf("EACCES project must NOT be disclosed locally-available: %q", eaccessLine)
	}
	if !strings.Contains(eaccessLine, "failed") {
		t.Fatalf("EACCES project line %q must be failed", eaccessLine)
	}
}
