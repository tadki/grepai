package rpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRPGStore persists the RPG graph as a single row in the workspace's
// shared Postgres, mirroring trace.PostgresSymbolStore. See that store for
// the rationale: workspace mode should keep every derived index in the shared
// backend so any host with the DSN can serve queries.
type PostgresRPGStore struct {
	*GOBRPGStore

	pool      *pgxpool.Pool
	workspace string
	project   string
}

const pgRPGIndexSchema = `CREATE TABLE IF NOT EXISTS grepai_workspace_indexes (
	workspace TEXT NOT NULL,
	project TEXT NOT NULL,
	kind TEXT NOT NULL,
	data BYTEA NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (workspace, project, kind)
)`

// NewPostgresRPGStore creates a postgres-backed RPG store for one workspace
// project. The row is keyed (workspace, project, 'rpg').
func NewPostgresRPGStore(ctx context.Context, dsn, workspace, project string) (*PostgresRPGStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, pgRPGIndexSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ensure workspace index schema: %w", err)
	}
	return &PostgresRPGStore{
		GOBRPGStore: NewGOBRPGStore(""),
		pool:        pool,
		workspace:   workspace,
		project:     project,
	}, nil
}

// Load reads the graph from the shared store. A missing row leaves the graph
// empty (first run), mirroring a missing gob file.
func (s *PostgresRPGStore) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var buf []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM grepai_workspace_indexes WHERE workspace = $1 AND project = $2 AND kind = 'rpg'`,
		s.workspace, s.project,
	).Scan(&buf)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read rpg index: %w", err)
	}
	return s.decodeIndexBytes(buf)
}

// Persist writes the graph to the shared store.
func (s *PostgresRPGStore) Persist(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf, err := s.encodeIndexBytes()
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO grepai_workspace_indexes (workspace, project, kind, data, updated_at)
		 VALUES ($1, $2, 'rpg', $3, now())
		 ON CONFLICT (workspace, project, kind) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		s.workspace, s.project, buf,
	)
	if err != nil {
		return fmt.Errorf("failed to persist rpg index: %w", err)
	}
	return nil
}

// Close releases the connection pool. Unlike the GOB store it does NOT
// persist: read-only serving hosts must never race a writer's newer row with
// their (possibly stale) in-memory snapshot.
func (s *PostgresRPGStore) Close() error {
	s.pool.Close()
	return nil
}

// Compile-time interface check.
var _ RPGStore = (*PostgresRPGStore)(nil)
