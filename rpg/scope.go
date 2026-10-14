package rpg

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/yoanbernabeu/grepai/config"
)

// ErrRPGIndexEmpty reports that RPG is disabled or the index yields no data.
// Read paths map it to a friendly CLI error or a nil MCP store; it is
// distinct from ErrRPGIndexOutdated, which demands a rebuild.
var ErrRPGIndexEmpty = errors.New("RPG is not enabled or index is empty")

// LoadLocalRPGForRoot loads the single-project gob RPG graph for one project
// root in read-only mode. It treats a disabled config and an empty graph as
// ErrRPGIndexEmpty so callers can degrade or report with one sentinel.
func LoadLocalRPGForRoot(ctx context.Context, projectRoot string) (RPGStore, *QueryEngine, error) {
	cfg, err := config.Load(projectRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	if !cfg.RPG.Enabled {
		return nil, nil, ErrRPGIndexEmpty
	}

	store := NewReadOnlyGOBRPGStore(config.GetRPGIndexPath(projectRoot))
	if err := store.Load(ctx); errors.Is(err, ErrRPGIndexOutdated) {
		return nil, nil, ErrRPGIndexOutdated
	} else if err != nil {
		return nil, nil, fmt.Errorf("failed to load RPG: %w", err)
	}
	if store.GetGraph().Stats().TotalNodes == 0 {
		return nil, nil, ErrRPGIndexEmpty
	}
	return store, NewQueryEngine(store.GetGraph()), nil
}

// LoadWorkspaceRPG loads the RPG store of the first workspace project (or the
// explicitly named project) that has RPG enabled and a non-empty index.
// Shared by the CLI and the MCP server so both modes read through one code
// path. Invalid workspace/project names are reported verbatim so callers can
// surface clean errors; ErrRPGIndexEmpty means "nothing loadable". When no
// project is named on a multi-project workspace, the first non-empty project
// wins (established MCP semantics) and the implicit choice is logged.
func LoadWorkspaceRPG(ctx context.Context, workspaceName, projectName string) (RPGStore, *QueryEngine, error) {
	wsCfg, err := config.LoadWorkspaceConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load workspace config: %w", err)
	}
	if wsCfg == nil {
		return nil, nil, ErrRPGIndexEmpty
	}
	ws, err := wsCfg.GetWorkspace(workspaceName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve workspace %q: %w", workspaceName, err)
	}

	projects := ws.Projects
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
			return nil, nil, fmt.Errorf("project %q not found in workspace %q", projectName, workspaceName)
		}
	}

	usePostgres := ws.Store.Backend == "postgres" && ws.Store.Postgres.DSN != ""
	outdated := false
	var pgFailure error
	for _, p := range projects {
		if p.Path == "" {
			continue
		}
		var store RPGStore
		if usePostgres {
			// Postgres-backed workspaces keep the RPG graph in the shared
			// store too; the builder host owns the project config, so a
			// non-empty shared RPG row is the enablement signal for serving
			// hosts that don't have the project files locally.
			pgStore, pgErr := NewPostgresRPGStore(ctx, ws.Store.Postgres.DSN, ws.Name, p.Name)
			if pgErr != nil {
				log.Printf("Warning: skipping RPG for project %s: failed to create postgres store: %v", p.Name, pgErr)
				pgFailure = pgErr
				continue
			}
			store = pgStore
		} else {
			cfg, cfgErr := config.Load(p.Path)
			if cfgErr != nil {
				log.Printf("Warning: skipping RPG for project %s: failed to load config: %v", p.Name, cfgErr)
				continue
			}
			if !cfg.RPG.Enabled {
				continue
			}
			store = NewReadOnlyGOBRPGStore(config.GetRPGIndexPath(p.Path))
		}
		if err := store.Load(ctx); err != nil {
			// PostgresRPGStore.Close only tears down its pool, but keep the
			// explicit release for symmetry; the gob store's Close persists,
			// so it is intentionally NOT closed after a failed load.
			if closer, ok := store.(*PostgresRPGStore); ok {
				closer.Close()
			}
			if errors.Is(err, ErrRPGIndexOutdated) {
				// If no project ends up loadable, the outdated hint is more
				// actionable than "not enabled".
				outdated = true
			} else {
				log.Printf("Warning: skipping RPG for project %s: %v", p.Name, err)
			}
			continue
		}
		if store.GetGraph().Stats().TotalNodes == 0 {
			if closer, ok := store.(*PostgresRPGStore); ok {
				closer.Close()
			}
			continue
		}
		if len(ws.Projects) > 1 && projectName == "" {
			// CLI callers write log lines to stderr; MCP callers route them
			// to the server log — either way the implicit project choice
			// must be visible to the operator.
			log.Printf("workspace %q has multiple projects; serving %q (first non-empty RPG index)", workspaceName, p.Name)
		}
		return store, NewQueryEngine(store.GetGraph()), nil
	}
	if pgFailure != nil {
		// A backend failure is a reachability problem, not "nothing to
		// read" — surface it so the CLI can report cleanly and auto mode can
		// decide whether to degrade.
		return nil, nil, fmt.Errorf("workspace %q postgres store failed: %w", workspaceName, pgFailure)
	}
	if outdated {
		return nil, nil, ErrRPGIndexOutdated
	}
	return nil, nil, ErrRPGIndexEmpty
}

// LoadRPGForScope resolves which RPG index a query reads. An explicit
// workspace name is authoritative and fails cleanly (no local fallback).
// Without one, the current project is attributed to a configured workspace:
// postgres-backed membership reads the shared store (degrading to the local
// gob with a stderr warning when postgres is unreachable), and everything
// else keeps the single-project gob behavior.
func LoadRPGForScope(ctx context.Context, workspaceName, projectName string) (RPGStore, *QueryEngine, error) {
	if workspaceName != "" {
		return LoadWorkspaceRPG(ctx, workspaceName, projectName)
	}

	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		return nil, nil, err
	}
	wsCfg, wsErr := config.LoadWorkspaceConfig()
	if wsErr != nil {
		log.Printf("Warning: failed to load workspace config, using local RPG index: %v", wsErr)
		return LoadLocalRPGForRoot(ctx, projectRoot)
	}
	if wsCfg == nil {
		return LoadLocalRPGForRoot(ctx, projectRoot)
	}
	ws, proj, ok := wsCfg.FindOwningWorkspace(projectRoot)
	if !ok || ws.Store.Backend != "postgres" || ws.Store.Postgres.DSN == "" {
		return LoadLocalRPGForRoot(ctx, projectRoot)
	}

	store, qe, err := LoadWorkspaceRPG(ctx, ws.Name, proj.Name)
	if err == nil {
		return store, qe, nil
	}
	if errors.Is(err, ErrRPGIndexEmpty) || errors.Is(err, ErrRPGIndexOutdated) {
		// The shared store answered: it genuinely has nothing (yet) for this
		// project. Not a reachability problem — do not silently substitute a
		// possibly stale local graph.
		return nil, nil, err
	}
	// Postgres unreachable (or otherwise failing): degrade to the local gob
	// so the query still answers, with a visible warning that the graph may
	// be stale.
	log.Printf("Warning: workspace %q postgres unavailable, falling back to local RPG index: %v", ws.Name, err)
	return LoadLocalRPGForRoot(ctx, projectRoot)
}
