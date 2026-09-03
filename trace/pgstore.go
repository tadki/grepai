package trace

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresSymbolStore keeps the same in-memory index as the GOB store but
// persists it as a single row in the workspace's shared Postgres. Workspace
// mode already shares chunks/documents through the store backend; keeping the
// symbol index there too lets any host with the DSN serve trace/refs queries
// without the project files being present locally.
type PostgresSymbolStore struct {
	*GOBSymbolStore

	pool      *pgxpool.Pool
	workspace string
	project   string
}

const pgSymbolIndexSchema = `CREATE TABLE IF NOT EXISTS grepai_workspace_indexes (
	workspace TEXT NOT NULL,
	project TEXT NOT NULL,
	kind TEXT NOT NULL,
	data BYTEA NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (workspace, project, kind)
)`

// NewPostgresSymbolStore creates a postgres-backed symbol store for one
// workspace project. The row is keyed (workspace, project, 'symbols').
func NewPostgresSymbolStore(ctx context.Context, dsn, workspace, project string) (*PostgresSymbolStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, pgSymbolIndexSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ensure workspace index schema: %w", err)
	}
	return &PostgresSymbolStore{
		GOBSymbolStore: NewGOBSymbolStore(""),
		pool:           pool,
		workspace:      workspace,
		project:        project,
	}, nil
}

// Load reads the index from the shared store. A missing row leaves the store
// empty (first run on a fresh workspace), mirroring a missing gob file.
func (s *PostgresSymbolStore) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var buf []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM grepai_workspace_indexes WHERE workspace = $1 AND project = $2 AND kind = 'symbols'`,
		s.workspace, s.project,
	).Scan(&buf)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read symbol index: %w", err)
	}
	return s.decodeIndexBytes(buf)
}

// Persist writes the index to the shared store.
func (s *PostgresSymbolStore) Persist(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf, err := s.encodeIndexBytes()
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO grepai_workspace_indexes (workspace, project, kind, data, updated_at)
		 VALUES ($1, $2, 'symbols', $3, now())
		 ON CONFLICT (workspace, project, kind) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		s.workspace, s.project, buf,
	)
	if err != nil {
		return fmt.Errorf("failed to persist symbol index: %w", err)
	}
	return nil
}

// Close releases the connection pool. Unlike the GOB store it does NOT
// persist: read-only serving hosts must never race a writer's newer row with
// their (possibly stale) in-memory snapshot.
func (s *PostgresSymbolStore) Close() error {
	s.pool.Close()
	return nil
}

// Compile-time interface check.
var _ SymbolStore = (*PostgresSymbolStore)(nil)
