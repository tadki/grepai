package trace

import (
	"context"
	"os"
	"testing"
)

func TestPostgresSymbolStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("GREPAI_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("GREPAI_POSTGRES_TEST_DSN is not set")
	}

	ctx := context.Background()
	writer, err := NewPostgresSymbolStore(ctx, dsn, "ws_pgstore_test", "proj_a")
	if err != nil {
		t.Fatalf("failed to create writer store: %v", err)
	}

	syms := []Symbol{
		{Name: "activate_node", Kind: KindFunction, File: "autoload/sane.gd", Line: 10, Language: "gdscript"},
	}
	refs := []Reference{
		{SymbolName: "node_id", Kind: RefKindRead, File: "ui.gd", Line: 3, CallerName: "_ready"},
		{SymbolName: "node_id", Kind: RefKindWrite, File: "ui.gd", Line: 7, CallerName: "_ready"},
	}
	if err := writer.SaveFileWithSignature(ctx, "ui.gd", "hash-1", "regex-test", syms, refs); err != nil {
		t.Fatalf("SaveFileWithSignature failed: %v", err)
	}
	if err := writer.Persist(ctx); err != nil {
		t.Fatalf("Persist failed: %v", err)
	}
	// Close on a writer's helper must not wipe the row (Close never persists).
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// A fresh reader (as a serving host would create) sees the same data.
	reader, err := NewPostgresSymbolStore(ctx, dsn, "ws_pgstore_test", "proj_a")
	if err != nil {
		t.Fatalf("failed to create reader store: %v", err)
	}
	defer reader.Close()
	if err := reader.Load(ctx); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	got, err := reader.LookupSymbol(ctx, "activate_node")
	if err != nil || len(got) != 1 || got[0].File != "autoload/sane.gd" {
		t.Fatalf("LookupSymbol = %v, %v; want one sane.gd symbol", got, err)
	}
	readers, err := reader.LookupReaders(ctx, "node_id")
	if err != nil || len(readers) != 1 {
		t.Fatalf("LookupReaders = %v, %v; want 1", readers, err)
	}
	writers, err := reader.LookupWriters(ctx, "node_id")
	if err != nil || len(writers) != 1 {
		t.Fatalf("LookupWriters = %v, %v; want 1", writers, err)
	}
	if v, ok := reader.GetFileExtractorVersion("ui.gd"); !ok || v != "regex-test" {
		t.Fatalf("GetFileExtractorVersion = %q, %v; want regex-test", v, ok)
	}
	if !reader.IsFileIndexed("ui.gd") {
		t.Fatal("IsFileIndexed(ui.gd) = false, want true")
	}
}
