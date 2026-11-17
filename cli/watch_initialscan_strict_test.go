package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/embedder"
	"github.com/yoanbernabeu/grepai/indexer"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
)

// failingSaveSymbolStore fails every SaveFileWithSignature so the strict
// symbol stage must surface the failure instead of warn+skip.
type failingSaveSymbolStore struct {
	projectSymbolStore
}

func (f *failingSaveSymbolStore) Load(ctx context.Context) error { return nil }
func (f *failingSaveSymbolStore) Persist(ctx context.Context) error {
	return nil
}
func (f *failingSaveSymbolStore) Close() error { return nil }
func (f *failingSaveSymbolStore) IsFileIndexed(filePath string) bool {
	return false
}
func (f *failingSaveSymbolStore) GetFileExtractorVersion(filePath string) (string, bool) {
	return "", false
}
func (f *failingSaveSymbolStore) GetFileContentHash(filePath string) (string, bool) {
	return "", false
}
func (f *failingSaveSymbolStore) SaveFileWithSignature(ctx context.Context, filePath string, contentHash, extractorVersion string, symbols []trace.Symbol, refs []trace.Reference) error {
	return fmt.Errorf("injected save failure for %s", filePath)
}
func (f *failingSaveSymbolStore) GetStats(ctx context.Context) (*trace.SymbolStats, error) {
	return &trace.SymbolStats{TotalSymbols: 0, TotalFiles: 0}, nil
}

// TestWorkspaceWatchSelectedInitialScanSymbolsFail pins that per-file symbol
// failures on a selected workspace project abort startup (strict mode)
// instead of warn+skip leaving a silently partial symbol index.
func TestWorkspaceWatchSelectedInitialScanSymbolsFail(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main\n\nfunc real() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	ignoreMatcher, err := indexer.NewIgnoreMatcher(proj, []string{}, "")
	if err != nil {
		t.Fatalf("ignore matcher: %v", err)
	}
	scanner := indexer.NewScanner(proj, ignoreMatcher)
	chunker := indexer.NewChunker(512, 50)
	vecStore := store.NewGOBStore(filepath.Join(proj, "index.gob"))
	idx := indexer.NewIndexer(proj, vecStore, &noOpEmbedder{}, chunker, scanner, time.Now().Add(1*time.Hour))
	extractor := trace.NewRegexExtractor()

	var store projectSymbolStore = &failingSaveSymbolStore{}

	_, err = runInitialScan(context.Background(), idx, scanner, extractor, store, []string{".go"}, time.Time{}, true, nil, nil, true)
	if err == nil {
		t.Fatal("expected strict initial scan to fail on injected symbol save failure")
	}
	if !strings.Contains(err.Error(), "symbol stage failed for 1 file(s)") {
		t.Fatalf("error %q lacks strict symbol stage summary", err.Error())
	}
	if !strings.Contains(err.Error(), "injected save failure") {
		t.Fatalf("error %q lacks injected reason", err.Error())
	}
}

// TestWorkspaceStatusAvailability pins the three disclosed availability
// states for workspace status: locally-available, serving-only and failed
// (with an explicit reason, including probe failures).
func TestWorkspaceStatusAvailability(t *testing.T) {
	cases := []struct {
		name       string
		statErr    error
		probeFound bool
		probeErr   error
		wantState  string
		wantReason bool
	}{
		{"local", nil, false, nil, "locally-available", false},
		{"local_ignores_probe_error", nil, false, fmt.Errorf("db down"), "locally-available", false},
		{"serving_only", os.ErrNotExist, true, nil, "serving-only", false},
		{"failed_absent_everywhere", os.ErrNotExist, false, nil, "failed", true},
		{"failed_probe_error", os.ErrNotExist, false, fmt.Errorf("db down"), "failed", true},
		{"failed_non_enoent_stat", fmt.Errorf("stat perm denied: EACCES"), true, nil, "failed", true},
		{"failed_non_enoent_stat_beats_probe", fmt.Errorf("stat perm denied: EACCES"), false, nil, "failed", true},
	}
	for _, tc := range cases {
		state, reason := classifyWorkspaceProjectAvailability(tc.statErr, tc.probeFound, tc.probeErr)
		if state != tc.wantState {
			t.Fatalf("%s: state = %q, want %q", tc.name, state, tc.wantState)
		}
		if tc.wantReason && reason == "" {
			t.Fatalf("%s: failed state must carry a reason", tc.name)
		}
		if !tc.wantReason && reason != "" {
			t.Fatalf("%s: non-failed state must not carry a reason, got %q", tc.name, reason)
		}
	}
	if _, reason := classifyWorkspaceProjectAvailability(os.ErrNotExist, false, fmt.Errorf("db down")); !strings.Contains(reason, "db down") {
		t.Fatalf("probe error must surface as the failure reason, got %q", reason)
	}
}

// noOpEmbedder is defined in watch_initialscan_test.go; keep a compile-time
// reference so this file documents its dependency without redefining it.
var _ embedder.Embedder = (*noOpEmbedder)(nil)
