package trace

import (
	"context"
	"fmt"

	"github.com/yoanbernabeu/grepai/config"
)

// LoadWorkspaceSymbolStores loads GOBSymbolStores for workspace projects.
// If projectName is non-empty, only that project's store is loaded.
func LoadWorkspaceSymbolStores(ctx context.Context, workspaceName, projectName string) ([]SymbolStore, error) {
	wsCfg, err := config.LoadWorkspaceConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load workspace config: %w", err)
	}
	if wsCfg == nil {
		return nil, fmt.Errorf("no workspaces configured; create one with: grepai workspace create <name>")
	}

	ws, err := wsCfg.GetWorkspace(workspaceName)
	if err != nil {
		return nil, err
	}

	var projects []config.ProjectEntry
	if projectName != "" {
		found := false
		for _, p := range ws.Projects {
			if p.Name == projectName {
				projects = []config.ProjectEntry{p}
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("project %q not found in workspace %q", projectName, workspaceName)
		}
	} else {
		projects = ws.Projects
	}

	stores := make([]SymbolStore, 0, len(projects))
	for _, p := range projects {
		var ss SymbolStore
		if ws.Store.Backend == "postgres" && ws.Store.Postgres.DSN != "" {
			// Postgres-backed workspaces keep the symbol index in the shared
			// store too, so serving hosts don't need the project files
			// locally (workspace mode with a remote builder).
			pgStore, err := NewPostgresSymbolStore(ctx, ws.Store.Postgres.DSN, ws.Name, p.Name)
			if err != nil {
				CloseSymbolStores(stores)
				return nil, fmt.Errorf("failed to create postgres symbol store for project %s: %w", p.Name, err)
			}
			ss = pgStore
		} else {
			ss = NewGOBSymbolStore(config.GetSymbolIndexPath(p.Path))
		}
		if err := ss.Load(ctx); err != nil {
			ss.Close()
			CloseSymbolStores(stores)
			return nil, fmt.Errorf("failed to load symbol index for project %s: %w", p.Name, err)
		}
		stores = append(stores, ss)
	}
	return stores, nil
}

// CloseSymbolStores closes all symbol stores in the slice.
func CloseSymbolStores(stores []SymbolStore) {
	for _, s := range stores {
		s.Close()
	}
}
