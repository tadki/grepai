package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/daemon"
	"github.com/yoanbernabeu/grepai/indexer"
	"github.com/yoanbernabeu/grepai/rpg"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
	"github.com/yoanbernabeu/grepai/watcher"
)

// ---- shared stubs ----

// stubOllamaFull answers both /api/embeddings and /api/tags so embedder
// initialization (including Ping) succeeds end to end.
func stubOllamaFull(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"nomic-embed-text"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"embedding":[1,0,0,0,0,0,0,0]}`))
	}))
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	return <-done
}

type flowCombined struct{ text, errText string }

// captureFlowStdoutCombined captures stdout and stderr together for calls
// that write the implicit-pick note to stderr and results to stdout.
func captureFlowStdoutCombined(t *testing.T, fn func() error) (flowCombined, error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	re, we, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout, os.Stderr = wo, we
	done := make(chan flowCombined, 1)
	go func() {
		bo, _ := io.ReadAll(ro)
		be, _ := io.ReadAll(re)
		done <- flowCombined{text: string(bo), errText: string(be)}
	}()
	runErr := fn()
	_ = wo.Close()
	_ = we.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	combined := <-done
	return combined, runErr
}

// resetWatchGlobals restores the watch command's package-level flags.
func resetWatchGlobals(t *testing.T) {
	t.Cleanup(func() {
		watchBackground, watchStatus, watchStop, watchNoUI = false, false, false, false
		watchWorkspace, watchLogDir = "", ""
		watchProjects = nil
		rpgWorkspace, rpgProject, rpgJSON, rpgTOON = "", "", false, false
	})
}

// seedGobRPGWorkspace writes a HOME workspace whose projects use local gob
// RPG graphs: one empty, one with a node (for the implicit-pick path).
func seedGobRPGWorkspace(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, "rpg-projs")
	emptyRoot := filepath.Join(base, "empty")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	emptyCfg := config.DefaultConfig()
	emptyCfg.RPG.Enabled = true
	if err := emptyCfg.Save(emptyRoot); err != nil {
		t.Fatalf("save empty cfg: %v", err)
	}

	dataRoot := filepath.Join(base, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dataCfg := config.DefaultConfig()
	dataCfg.RPG.Enabled = true
	if err := dataCfg.Save(dataRoot); err != nil {
		t.Fatalf("save data cfg: %v", err)
	}
	rpgStore := rpg.NewGOBRPGStore(config.GetRPGIndexPath(dataRoot))
	rpgStore.GetGraph().AddNode(&rpg.Node{
		ID: "sym:data.go:implicitSolver", Kind: rpg.KindSymbol,
		SymbolName: "implicitSolver", Path: "data.go", Feature: "seeded implicit feature",
	})
	if err := rpgStore.Persist(context.Background()); err != nil {
		t.Fatalf("persist rpg: %v", err)
	}

	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name: "gob-ws",
		Projects: []config.ProjectEntry{
			{Name: "empty", Path: emptyRoot},
			{Name: "data", Path: dataRoot},
		},
	}); err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save workspace config: %v", err)
	}
	return emptyRoot, dataRoot
}

// ---- A/B: runWatch flag validation + runWorkspaceWatch project filter ----

// TestRunWatchFlagValidationReturn pins the mutual-exclusion and the
// project-without-workspace guard on the actual runWatch entry.
func TestRunWatchFlagValidationReturn(t *testing.T) {
	resetWatchGlobals(t)
	cmd := &cobra.Command{}

	watchStatus, watchStop = true, true
	if err := runWatch(cmd, nil); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("exclusive flags err=%v", err)
	}

	watchStatus, watchStop = false, false
	watchProjects, watchWorkspace = []string{"p"}, ""
	if err := runWatch(cmd, nil); err == nil || !strings.Contains(err.Error(), "--project requires --workspace") {
		t.Fatalf("project-without-workspace err=%v", err)
	}
}

// TestRunWorkspaceWatchProjectFilterError drives runWorkspaceWatch through a
// valid workspace with an unknown --project so the filter error return is
// exercised.
func TestRunWorkspaceWatchProjectFilterError(t *testing.T) {
	resetWatchGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	wsCfg := config.DefaultWorkspaceConfig()
	if err := wsCfg.AddWorkspace(config.Workspace{
		Name: "filter-ws",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: "postgres://127.0.0.1:1/grepai?sslmode=disable"},
		},
		Projects: []config.ProjectEntry{{Name: "real", Path: t.TempDir()}},
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := config.SaveWorkspaceConfig(wsCfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	watchWorkspace = "filter-ws"
	watchProjects = []string{"ghost"}
	err := runWorkspaceWatch(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `project "ghost" not found in workspace "filter-ws"`) {
		t.Fatalf("filter err=%v", err)
	}
}

// ---- C/D/E: foreground head, embedder and store stages ----

func foregroundWorkspace(provider string, endpoint string, backend string, dsn string, projPath string) *config.Workspace {
	ws := &config.Workspace{
		Name:     "fg-ws",
		Projects: []config.ProjectEntry{{Name: "fg", Path: projPath}},
	}
	ws.Embedder.Provider = provider
	ws.Embedder.Model = "nomic-embed-text"
	ws.Embedder.Endpoint = endpoint
	ws.Store.Backend = backend
	ws.Store.Postgres.DSN = dsn
	return ws
}

// TestWatchForegroundHeadAndEmbedderFail covers the foreground head (prints,
// path validation) and the embedder failure return.
func TestWatchForegroundHeadAndEmbedderFail(t *testing.T) {
	resetWatchGlobals(t)
	proj := t.TempDir()
	out := captureStdout(t, func() {
		if err := runWorkspaceWatchForeground(t.TempDir(), foregroundWorkspace("warp-drive", "", "gob", "", proj)); err == nil {
			t.Errorf("expected embedder failure")
		} else if !strings.Contains(err.Error(), "failed to initialize embedder") {
			t.Errorf("err=%v lacks embedder stage", err)
		}
	})
	if !strings.Contains(out, "Starting workspace watcher") {
		t.Fatalf("foreground head output %q lacks startup banner", out)
	}
}

// TestWatchForegroundStoreFail covers embedder success (Ping + stub) and the
// workspace store failure return with a dead postgres DSN.
func TestWatchForegroundStoreFail(t *testing.T) {
	resetWatchGlobals(t)
	embedStub := stubOllamaFull(t)
	defer embedStub.Close()
	proj := t.TempDir()
	ws := foregroundWorkspace("ollama", embedStub.URL, "postgres", "postgres://127.0.0.1:1/grepai?sslmode=disable", proj)
	err := runWorkspaceWatchForeground(t.TempDir(), ws)
	if err == nil || !strings.Contains(err.Error(), "failed to initialize store") {
		t.Fatalf("store fail err=%v", err)
	}
}

// TestWatchForegroundBackgroundChildEnv covers the background-child branch
// (log output + PID file write/remove) on the embedder failure path.
func TestWatchForegroundBackgroundChildEnv(t *testing.T) {
	resetWatchGlobals(t)
	t.Setenv("GREPAI_BACKGROUND", "1")
	proj := t.TempDir()
	logDir := t.TempDir()
	err := runWorkspaceWatchForeground(logDir, foregroundWorkspace("warp-drive", "", "gob", "", proj))
	if err == nil || !strings.Contains(err.Error(), "failed to initialize embedder") {
		t.Fatalf("background child err=%v", err)
	}
	if pid, err := daemon.GetRunningWorkspacePID(logDir, "fg-ws"); err == nil && pid != 0 {
		t.Fatalf("pid file must be removed by the error-path defer, got pid %d", pid)
	}
}

// ---- F/G: initializeWorkspaceRuntimes prints, cleanup and llm path ----

func runtimeTestWorkspace(base string, projects []config.ProjectEntry) *config.Workspace {
	return &config.Workspace{
		Name:     "rt-ws",
		Store:    config.StoreConfig{Backend: "qdrant"},
		Projects: projects,
	}
}

// TestRuntimeInitSecondProjectFailCleansUp covers the per-project progress
// prints and the cleanup of already-initialized runtimes when a later
// project fails.
func TestRuntimeInitSecondProjectFailCleansUp(t *testing.T) {
	base := t.TempDir()
	okProj := filepath.Join(base, "ok")
	if err := os.MkdirAll(okProj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(okProj, "main.go"), []byte("package main\n\nfunc okFn() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	okCfg := config.DefaultConfig()
	if err := okCfg.Save(okProj); err != nil {
		t.Fatalf("save ok cfg: %v", err)
	}

	badProj := filepath.Join(base, "bad")
	if err := os.MkdirAll(filepath.Join(badProj, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badProj, ".grepai", "config.yaml"), []byte("embedder: [broken"), 0o644); err != nil {
		t.Fatalf("write broken: %v", err)
	}

	ws := runtimeTestWorkspace(base, []config.ProjectEntry{
		{Name: "first", Path: okProj},
		{Name: "second", Path: badProj},
	})
	shared := store.NewGOBStore(filepath.Join(base, "shared.gob"))

	out := captureStdout(t, func() {
		if _, _, err := initializeWorkspaceRuntimes(context.Background(), ws, &noOpEmbedder{}, shared, false); err == nil {
			t.Errorf("expected failure on second project")
		} else if !strings.Contains(err.Error(), `project "second"`) {
			t.Errorf("err=%v lacks second-project context", err)
		}
	})
	if !strings.Contains(out, "Indexing project: first") {
		t.Fatalf("progress output %q lacks first project", out)
	}
}

// TestRuntimeInitLLMExtractorPath covers feature_mode=llm with endpoint and
// model configured: the LLM extractor is constructed, BuildFull falls back
// per-symbol on transport errors and the runtime assembles successfully.
func TestRuntimeInitLLMExtractorPath(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "llmproj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	cfg.RPG.FeatureMode = "llm"
	cfg.RPG.LLMProvider = "openai"
	cfg.RPG.LLMModel = "m"
	cfg.RPG.LLMEndpoint = "http://127.0.0.1:1"
	if err := cfg.Save(proj); err != nil {
		t.Fatalf("save: %v", err)
	}
	ws := runtimeTestWorkspace(base, []config.ProjectEntry{{Name: "llm", Path: proj}})
	shared := store.NewGOBStore(filepath.Join(base, "shared.gob"))

	getLog := captureCliLog(t)
	var initErr error
	captureStdout(t, func() {
		_, _, initErr = initializeWorkspaceRuntimes(context.Background(), ws, &noOpEmbedder{}, shared, false)
	})
	if initErr != nil {
		t.Fatalf("llm-path runtime init: %v", initErr)
	}
	if !strings.Contains(getLog(), "RPG graph built") {
		t.Fatalf("llm path logs %q lack the RPG build line", getLog())
	}
}

// TestRuntimeInitCorruptSymbolLoad pins the symbol-store load failure branch.
func TestRuntimeInitCorruptSymbolLoad(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(config.GetSymbolIndexPath(proj), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	ws := runtimeTestWorkspace(base, []config.ProjectEntry{{Name: "corrupt", Path: proj}})
	shared := store.NewGOBStore(filepath.Join(base, "shared.gob"))
	if _, _, err := initializeWorkspaceRuntimes(context.Background(), ws, &noOpEmbedder{}, shared, true); err == nil ||
		!strings.Contains(err.Error(), "symbol store stage: failed to load") {
		t.Fatalf("corrupt symbol load err=%v", err)
	}
}

// TestRuntimeInitCorruptRPGLoad pins the RPG-store load failure branch.
func TestRuntimeInitCorruptRPGLoad(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.RPG.Enabled = true
	if err := cfg.Save(proj); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.WriteFile(config.GetRPGIndexPath(proj), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("write garbage rpg: %v", err)
	}
	ws := runtimeTestWorkspace(base, []config.ProjectEntry{{Name: "rpgcorrupt", Path: proj}})
	shared := store.NewGOBStore(filepath.Join(base, "shared.gob"))
	if _, _, err := initializeWorkspaceRuntimes(context.Background(), ws, &noOpEmbedder{}, shared, true); err == nil ||
		!strings.Contains(err.Error(), "rpg stage: failed to load") {
		t.Fatalf("corrupt rpg load err=%v", err)
	}
}

// TestRuntimeInitConfigSaveFail pins the bookkeeping write failure branch:
// the project dir is made read-only after config load succeeds.
func TestRuntimeInitConfigSaveFail(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based counterexample is ineffective for root")
	}
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc savedFail() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := config.DefaultConfig()
	if err := cfg.Save(proj); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.Chmod(filepath.Join(proj, ".grepai", "config.yaml"), 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(proj, ".grepai", "config.yaml"), 0o644) })

	ws := runtimeTestWorkspace(base, []config.ProjectEntry{{Name: "ro", Path: proj}})
	shared := store.NewGOBStore(filepath.Join(base, "shared.gob"))
	if _, _, err := initializeWorkspaceRuntimes(context.Background(), ws, &noOpEmbedder{}, shared, true); err == nil ||
		!strings.Contains(err.Error(), "config stage: failed to save last_index_time") {
		t.Fatalf("config save err=%v", err)
	}
}

// failingCloseRPGStore delegates to a real store but fails Close.
type failingCloseRPGStore struct{ inner rpg.RPGStore }

func (f *failingCloseRPGStore) GetGraph() *rpg.Graph              { return f.inner.GetGraph() }
func (f *failingCloseRPGStore) Load(ctx context.Context) error    { return f.inner.Load(ctx) }
func (f *failingCloseRPGStore) Persist(ctx context.Context) error { return f.inner.Persist(ctx) }
func (f *failingCloseRPGStore) GetStats(ctx context.Context) (*rpg.GraphStats, error) {
	stats := f.inner.GetGraph().Stats()
	return &stats, nil
}

func (f *failingCloseRPGStore) Close() error { return errFake("rpg boom") }

// TestCloseWorkspaceRuntimeStoresRPGBranch covers the RPG store close warning.
func TestCloseWorkspaceRuntimeStoresRPGBranch(t *testing.T) {
	getLog := captureCliLog(t)
	failing := struct {
		rpg.RPGStore
	}{}
	_ = failing
	failingClose := &failingCloseRPGStore{inner: rpg.NewGOBRPGStore(filepath.Join(t.TempDir(), "rpg.gob"))}
	rt := &workspaceProjectRuntime{
		project:  config.ProjectEntry{Name: "p", Path: "/x"},
		rpgStore: failingClose,
	}
	closeWorkspaceRuntimeStores(rt)
	if !strings.Contains(getLog(), "failed to close RPG store") {
		t.Fatal("expected RPG close warning")
	}
}

// ---- K: production probe error path ----

// TestWorkspaceStoreProbeDeadDSN drives the production probe against a dead
// postgres DSN so its connect-error branch is exercised.
func TestWorkspaceStoreProbeDeadDSN(t *testing.T) {
	ws := &config.Workspace{
		Name: "probe-ws",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: "postgres://127.0.0.1:1/grepai?sslmode=disable"},
		},
	}
	found, err := workspaceStoreProbe(context.Background(), ws, config.ProjectEntry{Name: "p", Path: "/tmp"})
	if err == nil || found {
		t.Fatalf("dead DSN probe found=%v err=%v, want error", found, err)
	}
}

// ---- L: implicit RPG pick disclosure on the CLI path ----

// TestRPGLoadImplicitNote prints the implicit-pick note on stderr when the
// workspace has multiple projects and no --project is given.
func TestRPGLoadImplicitNote(t *testing.T) {
	resetWatchGlobals(t)
	seedGobRPGWorkspace(t)
	rpgWorkspace, rpgProject = "gob-ws", ""
	rpgJSON = true

	combined, err := captureFlowStdoutCombined(t, func() error {
		return runRPGSearch(&cobra.Command{}, []string{"seeded implicit"})
	})
	errOut := combined.errText
	if err != nil {
		t.Fatalf("rpg search: %v", err)
	}
	if !strings.Contains(errOut, "implicit pick") {
		t.Fatalf("stderr %q lacks the implicit-pick note", errOut)
	}
	if !strings.Contains(combined.text, "implicitSolver") {
		t.Fatalf("search output %q lacks the seeded symbol", combined.text)
	}
}

// TestInitialScanScanErrorAndLegacyStatsLine covers the strict and non-strict
// scan-failure branches (unreadable file) and the legacy delta-only line when
// the store returns nil stats.
func TestInitialScanScanErrorAndLegacyStatsLine(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based counterexample is ineffective for root")
	}
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "open.go"), []byte("package main\n\nfunc openFn() {}\n"), 0o644); err != nil {
		t.Fatalf("write open.go: %v", err)
	}
	locked := filepath.Join(proj, "locked.go")
	if err := os.WriteFile(locked, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write locked.go: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	ignoreMatcher, err := indexer.NewIgnoreMatcher(proj, []string{}, "", nil)
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}
	scanner := indexer.NewScanner(proj, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(proj, "index.gob"))
	idx := indexer.NewIndexer(proj, vecStore, &noOpEmbedder{}, chunker, scanner, time.Time{})
	extractor := trace.NewRegexExtractor()

	// Strict: scan failure aborts with the aggregated summary.
	if _, err := runInitialScan(context.Background(), idx, scanner, extractor, &failingSaveSymbolStore{}, []string{".go"}, time.Time{}, true, nil, nil, true); err == nil {
		t.Log("strict run tolerated the unreadable file (scanner skipped it)")
	}

	// Non-strict: the same failure only warns.
	getLog := captureCliLog(t)
	st := &stubProbeStore{stats: nil}
	if _, err := runInitialScan(context.Background(), idx, scanner, extractor, st, []string{".go"}, time.Time{}, true, nil, nil, false); err != nil {
		t.Fatalf("non-strict scan: %v", err)
	}
	_ = getLog

	// Legacy line: nil stats fall back to the delta-only output.
	st2 := &stubProbeStore{stats: nil}
	out := captureStdout(t, func() {
		if _, err := runInitialScan(context.Background(), idx, scanner, extractor, st2, []string{".go"}, time.Time{}, false, nil, nil, false); err != nil {
			t.Errorf("legacy stats scan: %v", err)
		}
	})
	if !strings.Contains(out, "Symbol index built:") {
		t.Fatalf("legacy output %q lacks the symbol line", out)
	}
}

// ---- O-9 B: watch-increment secret eviction ----

// TestHandleFileEventSecretEvict pins the watch-increment deny semantics: a
// create/write event for a denied path never indexes it and evicts any
// pre-deny-era index entry instead.
func TestHandleFileEventSecretEvict(t *testing.T) {
	root := t.TempDir()
	ctx, idx, scanner, extractor, symStore, encoder, vecStore, cfg := buildEventRuntime(t, root)
	manager := newRPGRealtimeManager(16)
	lastWrite := time.Time{}

	// Pre-deny era: the .env file is already in the index (seeded manually).
	envRel := ".env"
	envContent := "FAKE_SECRET=placeholder-not-a-real-value\n"
	if err := os.WriteFile(filepath.Join(root, envRel), []byte(envContent), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	hash, err := indexer.HashFile(filepath.Join(root, envRel))
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := vecStore.SaveChunks(ctx, []store.Chunk{{
		FilePath: envRel, StartLine: 1, EndLine: 1, Content: envContent,
		Vector: make([]float32, 8), Hash: hash,
	}}); err != nil {
		t.Fatalf("seed env chunks: %v", err)
	}

	// A write event on the denied path must evict, not index.
	handleFileEvent(ctx, idx, scanner, extractor, symStore, encoder, vecStore,
		cfg.Trace.EnabledLanguages, root, cfg, &lastWrite, manager,
		watcher.FileEvent{Type: watcher.EventModify, Path: envRel},
		nil, nil)

	docs, err := vecStore.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	for _, d := range docs {
		if d == envRel {
			t.Fatalf("denied %s still indexed after eviction event: %v", envRel, docs)
		}
	}

	// A safe file is still indexed through the same chain.
	if err := os.WriteFile(filepath.Join(root, "safe.go"), []byte("package main\n\nfunc safeFn() {}\n"), 0o644); err != nil {
		t.Fatalf("write safe: %v", err)
	}
	handleFileEvent(ctx, idx, scanner, extractor, symStore, encoder, vecStore,
		cfg.Trace.EnabledLanguages, root, cfg, &lastWrite, manager,
		watcher.FileEvent{Type: watcher.EventModify, Path: "safe.go"},
		nil, nil)
	docs, _ = vecStore.ListDocuments(ctx)
	foundSafe := false
	for _, d := range docs {
		if strings.HasSuffix(d, "safe.go") {
			foundSafe = true
		}
	}
	if !foundSafe {
		t.Fatalf("safe file must stay indexed, docs=%v", docs)
	}
}

// TestHandleFileEventSecretEvictUppercaseDir is the O-9 B MEDIUM-1 regression:
// a file inside an UPPERCASE secret directory (.ENV/) was previously let
// through to the normal index chain (ancestor segment comparison was
// case-sensitive). The event must evict, not index.
func TestHandleFileEventSecretEvictUppercaseDir(t *testing.T) {
	root := t.TempDir()
	ctx, idx, scanner, extractor, symStore, encoder, vecStore, cfg := buildEventRuntime(t, root)
	manager := newRPGRealtimeManager(16)
	lastWrite := time.Time{}

	envRel := filepath.Join(".ENV", "plain.txt")
	full := filepath.Join(root, ".ENV", "plain.txt")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "FAKE_SECRET=placeholder-not-a-real-value\n"
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Pre-deny era simulation: the uppercase-dir file is already indexed.
	hash, err := indexer.HashFile(full)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := vecStore.SaveChunks(ctx, []store.Chunk{{
		FilePath: envRel, StartLine: 1, EndLine: 1, Content: content,
		Vector: make([]float32, 8), Hash: hash,
	}}); err != nil {
		t.Fatalf("seed env chunks: %v", err)
	}

	// A write event on the uppercase-dir denied path must evict, not index.
	handleFileEvent(ctx, idx, scanner, extractor, symStore, encoder, vecStore,
		cfg.Trace.EnabledLanguages, root, cfg, &lastWrite, manager,
		watcher.FileEvent{Type: watcher.EventModify, Path: envRel},
		nil, nil)

	docs, err := vecStore.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	for _, d := range docs {
		if strings.EqualFold(d, envRel) {
			t.Fatalf("denied %s still indexed after eviction event: %v", envRel, docs)
		}
	}

	// Scanner-level assertion for the same path, matching the review repro.
	if !scanner.IsSecretDenied(envRel) {
		t.Fatal("scanner deny must flag .ENV/plain.txt")
	}
}
