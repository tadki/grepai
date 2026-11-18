package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/indexer"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
	"github.com/yoanbernabeu/grepai/watcher"
)

// Tests added by the SEE-1597 O-9 B pre-review hardener: each kills a
// specific surviving mutant from the isolated matrix at 046900c
// (see see1597-h1-hardener.json on the issue). The existing evict tests seed
// chunks only, which ListDocuments cannot observe, so eviction outcomes need
// these document-, symbol- and disclosure-level assertions.

// TestHandleFileEventSecretEvictRealDocs seeds a real document so the
// eviction observably removes it (kills EV1-earlyreturn-dropped and
// EV2-removefile-dropped).
func TestHandleFileEventSecretEvictRealDocs(t *testing.T) {
	root := t.TempDir()
	ctx, idx, scanner, extractor, symStore, encoder, vecStore, cfg := buildEventRuntime(t, root)
	manager := newRPGRealtimeManager(16)
	lastWrite := time.Time{}

	envRel := ".env"
	if err := os.WriteFile(filepath.Join(root, envRel), []byte("FAKE_SECRET=placeholder-not-a-real-value\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	hash, err := indexer.HashFile(filepath.Join(root, envRel))
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := vecStore.SaveDocument(ctx, store.Document{
		Path:     envRel,
		Hash:     hash,
		ModTime:  time.Now(),
		ChunkIDs: []string{"o9b-seed-chunk-1"},
	}); err != nil {
		t.Fatalf("seed document: %v", err)
	}

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
			t.Fatalf("denied %s must be evicted from documents, docs=%v", envRel, docs)
		}
	}
}

// TestHandleFileEventSecretEvictSymbolsDisclosures pins the symbol-store
// deletion and the denied-path activity/stats disclosures (kills
// EV3-symboldelete-dropped, EV4-activity-dropped, EV5-stats-dropped).
func TestHandleFileEventSecretEvictSymbolsDisclosures(t *testing.T) {
	root := t.TempDir()
	ctx, idx, scanner, extractor, symStore, encoder, vecStore, cfg := buildEventRuntime(t, root)
	manager := newRPGRealtimeManager(16)
	lastWrite := time.Time{}

	envRel := ".env"
	if err := os.WriteFile(filepath.Join(root, envRel), []byte("FAKE_SECRET=placeholder-not-a-real-value\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	hash, err := indexer.HashFile(filepath.Join(root, envRel))
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := symStore.SaveFileWithSignature(ctx, envRel, hash, "v1",
		[]trace.Symbol{{Name: "evictedSym", Kind: trace.KindFunction, File: envRel, Language: "go"}}, nil); err != nil {
		t.Fatalf("seed symbols: %v", err)
	}
	if err := symStore.Persist(ctx); err != nil {
		t.Fatalf("persist symbols: %v", err)
	}

	var acts []string
	var deltas []watchStatsDelta
	handleFileEvent(ctx, idx, scanner, extractor, symStore, encoder, vecStore,
		cfg.Trace.EnabledLanguages, root, cfg, &lastWrite, manager,
		watcher.FileEvent{Type: watcher.EventModify, Path: envRel},
		func(state, file string) { acts = append(acts, state+" "+file) },
		func(projectRoot string, d watchStatsDelta) { deltas = append(deltas, d) })

	syms, err := symStore.GetSymbolsForFile(ctx, envRel)
	if err != nil {
		t.Fatalf("GetSymbolsForFile: %v", err)
	}
	if len(syms) != 0 {
		t.Fatalf("symbols of a denied file must be deleted, got %d", len(syms))
	}
	joined := strings.Join(acts, "|")
	if !strings.Contains(joined, "denied .env") || !strings.Contains(joined, "steady ") {
		t.Fatalf("denied-path activity disclosures missing, acts=%v", acts)
	}
	if len(deltas) != 1 || deltas[0].FilesRemoved != 1 || deltas[0].SymbolsLost != 1 {
		t.Fatalf("denied-path stats disclosure wrong, deltas=%v", deltas)
	}
}

// TestWorkspaceWatchSecurityAllowWiring pins that the workspace runtime
// chain passes projectCfg.Security.Allow into the ignore matcher (kills
// WI2-workspace-nil; also parses security.allow through the real chain).
func TestWorkspaceWatchSecurityAllowWiring(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "app.env"), []byte("K=v\n"), 0o644); err != nil {
		t.Fatalf("write app.env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "real.env"), []byte("K=v\n"), 0o644); err != nil {
		t.Fatalf("write real.env: %v", err)
	}
	projCfg := config.DefaultConfig()
	projCfg.Security.Allow = []string{"app.env"}
	projCfg.Chunking.CustomExtensions = []string{".env"}
	if err := projCfg.Save(proj); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	ws := runtimeTestWorkspace(base, []config.ProjectEntry{{Name: "alw", Path: proj}})
	shared := store.NewGOBStore(filepath.Join(base, "shared.gob"))
	if _, _, err := initializeWorkspaceRuntimes(context.Background(), ws, &noOpEmbedder{}, shared, false); err != nil {
		t.Fatalf("initializeWorkspaceRuntimes: %v", err)
	}

	docs, err := shared.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	foundAllowed, foundDenied := false, false
	for _, d := range docs {
		if strings.HasSuffix(d, "app.env") {
			foundAllowed = true
		}
		if strings.HasSuffix(d, "real.env") {
			foundDenied = true
		}
	}
	if !foundAllowed {
		t.Fatalf("allowlisted app.env must be indexed via Security.Allow wiring, docs=%v", docs)
	}
	if foundDenied {
		t.Fatalf("non-allowlisted real.env must stay denied, docs=%v", docs)
	}
}

// TestSingleProjectWatchSecurityAllowWiring pins the single-project chain
// wiring (kills WI1-singleproject-nil): config.Load must carry
// security.allow into the watcher's ignore matcher.
func TestSingleProjectWatchSecurityAllowWiring(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Security.Allow = []string{"app.env"}
	cfg.Chunking.CustomExtensions = []string{".env"}
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save cfg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.env"), []byte("K=v\n"), 0o644); err != nil {
		t.Fatalf("write app.env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.env"), []byte("K=v\n"), 0o644); err != nil {
		t.Fatalf("write real.env: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- watchProjectWithEventObserver(ctx, root, &noOpEmbedder{}, true,
			nil, nil, nil, nil, nil,
			func(state, file string) {}, func(projectRoot string, d watchStatsDelta) {})
	}()
	select {
	case werr := <-errCh:
		if werr != nil && !strings.Contains(werr.Error(), "context deadline exceeded") {
			t.Fatalf("watcher exited with unexpected error: %v", werr)
		}
	case <-time.After(25 * time.Second):
		t.Fatalf("watcher did not exit within deadline")
	}

	st := store.NewGOBStore(config.GetIndexPath(root))
	if err := st.Load(context.Background()); err != nil {
		t.Fatalf("load persisted index: %v", err)
	}
	docs, err := st.ListDocuments(context.Background())
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	foundAllowed := false
	for _, d := range docs {
		if strings.HasSuffix(d, "app.env") {
			foundAllowed = true
		}
	}
	if !foundAllowed {
		t.Fatalf("allowlisted app.env must be indexed via single-project wiring, docs=%v", docs)
	}
}
