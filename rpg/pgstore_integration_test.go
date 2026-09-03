package rpg

import (
	"context"
	"os"
	"testing"
)

func TestPostgresRPGStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("GREPAI_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("GREPAI_POSTGRES_TEST_DSN is not set")
	}

	ctx := context.Background()
	writer, err := NewPostgresRPGStore(ctx, dsn, "ws_pgstore_test", "proj_a")
	if err != nil {
		t.Fatalf("failed to create writer store: %v", err)
	}
	graph := writer.GetGraph()
	graph.AddNode(&Node{ID: "sym:ui.gd:_ready", Kind: KindSymbol, Feature: "on-ready"})
	if err := writer.Persist(ctx); err != nil {
		t.Fatalf("Persist failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reader, err := NewPostgresRPGStore(ctx, dsn, "ws_pgstore_test", "proj_a")
	if err != nil {
		t.Fatalf("failed to create reader store: %v", err)
	}
	defer reader.Close()
	if err := reader.Load(ctx); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	stats := reader.GetGraph().Stats()
	if stats.TotalNodes != 1 {
		t.Fatalf("TotalNodes = %d, want 1", stats.TotalNodes)
	}
}
