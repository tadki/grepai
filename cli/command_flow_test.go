package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/rpg"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
)

// flowEmbedDim is the vector dimension used across the flow-test fixtures.
const flowEmbedDim = 8

// stubOllamaEmbedder answers /api/embeddings with a fixed unit vector so
// seeded chunks score a perfect match.
func stubOllamaEmbedder(t *testing.T) *httptest.Server {
	t.Helper()
	vec := make([]float32, flowEmbedDim)
	vec[0] = 1
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("unexpected embed path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":[1,0,0,0,0,0,0,0]}`))
		_ = vec
	}))
}

// flowEnv builds a HOME-scoped project with a seeded GOB vector index,
// symbol store and RPG graph, and an ollama-compatible embedder stub.
func flowEnv(t *testing.T) (projectRoot string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "flowproj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	embedStub := stubOllamaEmbedder(t)
	t.Cleanup(embedStub.Close)

	cfg := config.DefaultConfig()
	cfg.Embedder.Provider = "ollama"
	cfg.Embedder.Model = "nomic-embed-text"
	cfg.Embedder.Endpoint = embedStub.URL
	dims := flowEmbedDim
	cfg.Embedder.Dimensions = &dims
	cfg.Store.Backend = "gob"
	cfg.Trace.EnabledLanguages = []string{".go"}
	cfg.RPG.Enabled = true
	if err := cfg.Save(root); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc flowHandler() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	// Seed the vector index with one chunk matching the stub embedding.
	ctx := context.Background()
	vec := make([]float32, flowEmbedDim)
	vec[0] = 1
	gob := store.NewGOBStore(config.GetIndexPath(root))
	chunk := store.Chunk{
		FilePath:  "main.go",
		StartLine: 1,
		EndLine:   3,
		Content:   "func flowHandler() {}",
		Vector:    vec,
		Hash:      "flow-hash",
	}
	if err := gob.SaveChunks(ctx, []store.Chunk{chunk}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	if err := gob.SaveDocument(ctx, store.Document{Path: "main.go", Hash: "flow-hash"}); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	if err := gob.Persist(ctx); err != nil {
		t.Fatalf("persist vector index: %v", err)
	}

	// Seed the symbol store with symbols and caller/callee/reader/writer refs.
	extractor := trace.NewRegexExtractor()
	symStore := trace.NewGOBSymbolStore(config.GetSymbolIndexPath(root))
	symbols := []trace.Symbol{
		{Name: "flowHandler", Kind: trace.KindFunction, File: "main.go", Line: 3, Language: "go"},
		{Name: "flowCaller", Kind: trace.KindFunction, File: "main.go", Line: 7, Language: "go"},
	}
	refs := []trace.Reference{
		{SymbolName: "flowHandler", File: "main.go", Line: 7, Kind: trace.RefKindCall, Context: "flowHandler()", CallerName: "flowCaller", CallerFile: "main.go", CallerLine: 7},
		{SymbolName: "flowHandler", File: "main.go", Line: 9, Kind: trace.RefKindRead, Context: "x := flowHandler", CallerName: "flowCaller", CallerFile: "main.go", CallerLine: 9},
		{SymbolName: "flowHandler", File: "main.go", Line: 11, Kind: trace.RefKindWrite, Context: "flowHandler = nil", CallerName: "flowCaller", CallerFile: "main.go", CallerLine: 11},
	}
	if err := symStore.SaveFileWithSignature(ctx, "main.go", "flow-hash", extractor.Version(), symbols, refs); err != nil {
		t.Fatalf("seed symbols: %v", err)
	}
	if err := symStore.Persist(ctx); err != nil {
		t.Fatalf("persist symbols: %v", err)
	}
	if err := symStore.Close(); err != nil {
		t.Fatalf("close symbols: %v", err)
	}

	// Work from the project directory so FindProjectRoot resolves.
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	return root
}

// resetFlowFlags restores the package-level command flags after a test so
// later tests start from defaults.
func resetFlowFlags(t *testing.T, reset func()) {
	t.Helper()
	t.Cleanup(reset)
}

const rpgSeedNodeID = "sym:main.go:flowHandler"

// seedLocalRPGGraph writes a one-node gob RPG graph into the flow project's
// .grepai dir so the rpg commands have local data to answer from.
func seedLocalRPGGraph(t *testing.T) {
	t.Helper()
	root, err := config.FindProjectRoot()
	if err != nil {
		t.Fatalf("find project root: %v", err)
	}
	rpgStore := rpg.NewGOBRPGStore(config.GetRPGIndexPath(root))
	rpgStore.GetGraph().AddNode(&rpg.Node{
		ID:         rpgSeedNodeID,
		Kind:       rpg.KindSymbol,
		Feature:    "seeded feature flowHandler",
		SymbolName: "flowHandler",
		Path:       "main.go",
		UpdatedAt:  time.Now(),
	})
	if err := rpgStore.Persist(context.Background()); err != nil {
		t.Fatalf("persist seeded rpg: %v", err)
	}
}

func runGitFlow(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
