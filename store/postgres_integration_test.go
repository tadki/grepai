package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// isolationGuard enforces the test-isolation contract for pg-backed store
// tests: they must never share a database with a real workspace index. The
// DSN must point at a dedicated test database — either a separate database
// name, or a search_path/template override — because these tests create
// schema and rows in the public `chunks`/`documents` tables. A DSN aimed at
// a production/shared workspace store is rejected instead of silently
// TRUNCATE-ing it.
//
// Passing GREPAI_POSTGRES_TEST_ALLOW_SHARED_DSN=1 overrides the guard for
// throwaway instances where the operator accepts the blast radius.
func isolationGuard(t *testing.T, dsn string) {
	t.Helper()
	if os.Getenv("GREPAI_POSTGRES_TEST_ALLOW_SHARED_DSN") == "1" {
		return
	}
	if isSharedWorkspaceDSN(dsn) {
		t.Skipf("refusing to run schema/TRUNCATE-bearing store tests against a shared workspace DSN " +
			"(database name looks like the shared grepai index store); point GREPAI_POSTGRES_TEST_DSN " +
			"at an isolated test database, or set GREPAI_POSTGRES_TEST_ALLOW_SHARED_DSN=1 to override")
	}
}

// isSharedWorkspaceDSN heuristically detects the live shared store DSN shape
// (database "grepai" with no search_path override), which is where the kol /
// gqt workspace indexes live.
func isSharedWorkspaceDSN(dsn string) bool {
	u, err := url.Parse(dsn)
	if err != nil {
		// Unparseable DSN: be conservative and refuse.
		return true
	}
	db := u.Path
	if i := strings.Index(db, "?"); i >= 0 {
		db = db[:i]
	}
	db = strings.Trim(db, "/")
	if db != "grepai" {
		return false
	}
	// Allow an explicit non-public search_path even on the same instance.
	if strings.Contains(dsn, "search_path=") && !strings.Contains(dsn, "search_path=public") {
		return false
	}
	return true
}

func TestPostgresStoreLookupByContentHashScopesCacheIdentity(t *testing.T) {
	dsn := os.Getenv("GREPAI_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("GREPAI_POSTGRES_TEST_DSN is not set")
	}
	isolationGuard(t, dsn)

	ctx := context.Background()
	victim, err := NewPostgresStore(ctx, dsn, "proj_victim", 3, "model=text-embedding-3-large")
	if err != nil {
		t.Fatalf("failed to create victim store: %v", err)
	}
	defer victim.Close()

	attackerProject, err := NewPostgresStore(ctx, dsn, "proj_attacker", 3, "model=text-embedding-3-large")
	if err != nil {
		t.Fatalf("failed to create attacker project store: %v", err)
	}
	defer attackerProject.Close()

	attackerModel, err := NewPostgresStore(ctx, dsn, "proj_victim", 3, "model=text-embedding-3-small")
	if err != nil {
		t.Fatalf("failed to create attacker model store: %v", err)
	}
	defer attackerModel.Close()

	// No TRUNCATE: the test scopes every assertion by (project_id,
	// embedding_cache_namespace), so it coexists with any other rows in the
	// test database instead of wiping shared tables. Row cleanup is best
	// effort and scoped to this test's own keys.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = victim.pool.Exec(ctx,
			`DELETE FROM chunks WHERE project_id = $1 AND embedding_cache_namespace = $2`,
			"proj_victim", "model=text-embedding-3-large")
		_, _ = victim.pool.Exec(ctx,
			`DELETE FROM documents WHERE project_id = $1`, "proj_victim")
	})

	contentHash := fmt.Sprintf("sha256:shared-content-%d", time.Now().UnixNano())
	victimVector := []float32{9.9, 8.8, 7.7}
	if err := victim.SaveChunks(ctx, []Chunk{{
		ID:          "README.md_0",
		FilePath:    "README.md",
		StartLine:   1,
		EndLine:     1,
		Content:     "File: README.md\n\nShared content",
		Vector:      victimVector,
		Hash:        "victim-row",
		ContentHash: contentHash,
		UpdatedAt:   time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("failed to save victim chunk: %v", err)
	}

	if _, found, err := attackerProject.LookupByContentHash(ctx, contentHash); err != nil {
		t.Fatalf("cross-project lookup failed: %v", err)
	} else if found {
		t.Fatal("different project should not reuse victim project's cached vector")
	}

	if _, found, err := attackerModel.LookupByContentHash(ctx, contentHash); err != nil {
		t.Fatalf("cross-model lookup failed: %v", err)
	} else if found {
		t.Fatal("different embedding cache namespace should not reuse victim model's cached vector")
	}

	got, found, err := victim.LookupByContentHash(ctx, contentHash)
	if err != nil {
		t.Fatalf("victim lookup failed: %v", err)
	}
	if !found {
		t.Fatal("victim project should find its own cached vector")
	}
	if len(got) != len(victimVector) {
		t.Fatalf("victim vector length mismatch: got %d, want %d", len(got), len(victimVector))
	}
	for i := range got {
		if got[i] != victimVector[i] {
			t.Fatalf("victim vector mismatch at %d: got %v, want %v", i, got, victimVector)
		}
	}
}
