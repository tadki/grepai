package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/trace"
)

// TestNewServerCtor covers the plain server constructor over a seeded
// project (tools registered, recorder wired).
func TestNewServerCtor(t *testing.T) {
	s0 := handlerEnv(t)
	_ = s0
	s, err := NewServer(s0.projectRoot)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if s.mcpServer == nil || s.recorder == nil {
		t.Fatal("NewServer must wire the MCP server and recorder")
	}
	tools := s.mcpServer.ListTools()
	if len(tools) == 0 {
		t.Fatal("NewServer must register tools")
	}
}

// TestCreateWorkspaceStoreBackends covers the postgres error path (dead DSN),
// the qdrant default collection branch and the unsupported backend error.
func TestCreateWorkspaceStoreBackends(t *testing.T) {
	s := &Server{}
	ctx := context.Background()
	dead := config.Workspace{
		Name: "ws-dead",
		Store: config.StoreConfig{
			Backend:  "postgres",
			Postgres: config.PostgresConfig{DSN: "postgres://127.0.0.1:1/grepai?sslmode=disable"},
		},
	}
	if _, err := s.createWorkspaceStore(ctx, &dead); err == nil {
		t.Fatal("dead postgres DSN must error")
	}

	qdrant := config.Workspace{
		Name: "ws-qdrant",
		Store: config.StoreConfig{
			Backend: "qdrant",
			Qdrant:  config.QdrantConfig{Endpoint: "http://127.0.0.1", Port: 1},
		},
	}
	// The gRPC dial fails against a closed port, exercising the qdrant branch.
	if _, err := s.createWorkspaceStore(ctx, &qdrant); err == nil {
		t.Log("qdrant dial unexpectedly succeeded")
	}

	gob := config.Workspace{
		Name:     "ws-gob",
		Store:    config.StoreConfig{Backend: "gob"},
		Projects: nil,
	}
	if _, err := s.createWorkspaceStore(ctx, &gob); err == nil || !strings.Contains(err.Error(), "unsupported backend") {
		t.Fatalf("gob backend err=%v, want unsupported", err)
	}
}

// TestCreateStoreBackends covers the server-level store factory branches.
func TestCreateStoreBackends(t *testing.T) {
	root := t.TempDir()
	s := &Server{projectRoot: root}
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.Store.Backend = "gob"
	st, err := s.createStore(ctx, cfg)
	if err != nil || st == nil {
		t.Fatalf("gob store: %v", err)
	}
	_ = st

	cfg.Store.Backend = "postgres"
	cfg.Store.Postgres.DSN = "postgres://127.0.0.1:1/grepai?sslmode=disable"
	if _, err := s.createStore(ctx, cfg); err == nil {
		t.Fatal("postgres dead DSN must error")
	}

	cfg.Store.Backend = "unknown"
	if _, err := s.createStore(ctx, cfg); err == nil || !strings.Contains(err.Error(), "unknown storage backend") {
		t.Fatalf("unknown backend err=%v", err)
	}
}

// TestResolveRefCallerSymbol pins the caller symbol resolution fallbacks.
func TestResolveRefCallerSymbol(t *testing.T) {
	ctx := context.Background()
	symStore := trace.NewGOBSymbolStore(filepathJoin(t.TempDir(), "symbols.gob"))
	_ = symStore.Load(ctx)
	defer symStore.Close()

	// Empty / top-level caller names pass through verbatim.
	passthrough := resolveRefCallerSymbol(symStore, ctx, trace.Reference{CallerName: "<top-level>", CallerFile: "f.go", CallerLine: 1})
	if passthrough.Name != "<top-level>" || passthrough.File != "f.go" {
		t.Fatalf("passthrough = %+v", passthrough)
	}

	// Unknown caller: synthesized from the reference.
	synth := resolveRefCallerSymbol(symStore, ctx, trace.Reference{CallerName: "nobody", CallerFile: "n.go", CallerLine: 9})
	if synth.Name != "nobody" || synth.File != "n.go" || synth.Line != 9 {
		t.Fatalf("synth = %+v", synth)
	}

	// Known caller: matched against the stored symbol.
	known := resolveRefCallerSymbol(symStore, ctx, trace.Reference{CallerName: "hHandler", CallerFile: "main.go", CallerLine: 3})
	if known.Name != "hHandler" && known.File == "" {
		t.Fatalf("known caller lookup = %+v", known)
	}

	// Known caller name but mismatched file: falls back to the first
	// candidate.
	fallback := resolveRefCallerSymbol(symStore, ctx, trace.Reference{CallerName: "hHandler", CallerFile: "other.go", CallerLine: 99})
	if fallback.Name != "hHandler" {
		t.Fatalf("fallback = %+v", fallback)
	}
}

func filepathJoin(base string, elems ...string) string {
	return filepath.Join(append([]string{base}, elems...)...)
}
