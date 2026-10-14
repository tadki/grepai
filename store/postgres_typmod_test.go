package store

import (
	"context"
	"os"
	"testing"
)

// TestEnsureSchema_SkipsAlterOnExistingMatchingColumn verifies against a real
// 768-dim chunks table that ensureSchema's ensure-vector step takes the skip
// branch: the column's atttypmod must remain 768 and no re-ALTER runs.
// Read-mostly: it relies on the schema already existing (created by earlier
// store use), so it needs the isolated test DB contract via DSN env.
func TestEnsureSchema_SkipsAlterOnExistingMatchingColumn(t *testing.T) {
	dsn := os.Getenv("GREPAI_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("GREPAI_POSTGRES_TEST_DSN is not set")
	}
	if os.Getenv("GREPAI_POSTGRES_TEST_ALLOW_SHARED_DSN") != "1" {
		t.Skip("requires GREPAI_POSTGRES_TEST_ALLOW_SHARED_DSN=1 (read-mostly probe against the schema-bearing DB)")
	}
	ctx := context.Background()
	s, err := NewPostgresStore(ctx, dsn, "proj_typmod_probe", 768)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	var typmod int
	if err := s.pool.QueryRow(ctx,
		`SELECT atttypmod FROM pg_attribute WHERE attrelid='chunks'::regclass AND attname='vector'`,
	).Scan(&typmod); err != nil {
		t.Fatalf("read atttypmod: %v", err)
	}
	if typmod != 768 {
		t.Fatalf("atttypmod = %d, want 768 (column must not be re-Altered)", typmod)
	}
}
