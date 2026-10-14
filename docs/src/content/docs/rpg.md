---
title: Repository Planning Graph
description: Search and explore grepai's feature-level code graph from the CLI or MCP
---

grepai's Repository Planning Graph (RPG) connects files, symbols, chunks, and feature hierarchy nodes into a navigable code graph. Use it when semantic search finds relevant code but you also need surrounding feature, hierarchy, or dependency context.

## Prerequisites

RPG data is built by the watcher. Enable RPG in `.grepai/config.yaml`, then rebuild the index:

```yaml
rpg:
  enabled: true
```

```bash
grepai watch
```

If the RPG index is missing or stale, CLI commands will tell you to rebuild with `grepai watch`.

## Search RPG Nodes

Use `grepai rpg search` to find graph nodes by feature language:

```bash
grepai rpg search "command line search flow"
grepai rpg search "command line search flow" --kinds symbol,file --scope cli --limit 5
grepai rpg search "command line search flow" --json
```

Useful flags:

| Flag | Description |
|------|-------------|
| `--scope` | Limit results to a feature path such as `cli` or `rpg/query` |
| `--kinds` | Comma-separated node kinds: `area`, `category`, `subcategory`, `file`, `symbol`, `chunk` |
| `--limit`, `-n` | Maximum number of results |
| `--workspace` | Query a workspace's RPG index explicitly (see below) |
| `--project` | Pick a project inside `--workspace` |
| `--json` | Structured JSON output |
| `--toon`, `-t` | Token-efficient structured output |

## Workspace RPG Indexes

In workspace mode the watch daemon keeps each project's RPG graph in the shared store (postgres), not in the project's local `.grepai/` directory. The `rpg` commands resolve where to read the graph the same way `search`/`trace`/`refs` do:

- With `--workspace <name>` (and optionally `--project <name>`), the CLI queries that workspace's shared RPG index explicitly. Invalid workspace or project names fail with a clear error.
- Without flags, the current directory decides: if the project belongs to a postgres-backed workspace, the shared store is read (falling back to the local index with a warning when postgres is unreachable); otherwise the project's local gob index is used as before.

```bash
grepai rpg search "scenario runner" --workspace gqt --project godot-qa-toolkit
grepai rpg fetch "sym:cli/search.go:runSearch" --workspace gqt
grepai rpg explore "sym:cli/search.go:runSearch" --workspace gqt --direction forward
```

On a multi-project workspace queried without `--project`, the first project with a non-empty RPG index is served and the choice is logged. Read-only queries never write: they neither create nor refresh any `.grepai/*.gob` files.

## Fetch Node Context

Use `fetch` when you know a node ID and want its hierarchy and edges:

```bash
grepai rpg fetch "sym:cli/search.go:runSearch"
grepai rpg fetch "sym:cli/search.go:runSearch" --json
```

The output includes the node, feature path, parents, children, incoming edges, outgoing edges, and any available code preview.

## Explore Neighborhoods

Use `explore` to traverse dependencies around a starting node:

```bash
grepai rpg explore "sym:cli/search.go:runSearch"
grepai rpg explore "sym:cli/search.go:runSearch" --direction forward --depth 1 --edge-types invokes --json
```

Useful flags:

| Flag | Description |
|------|-------------|
| `--direction` | `forward`, `reverse`, or `both` |
| `--depth`, `-d` | BFS traversal depth |
| `--edge-types` | Comma-separated edge types: `feature_parent`, `contains`, `invokes`, `imports`, `maps_to_chunk`, `semantic_sim` |
| `--limit`, `-n` | Maximum nodes to return |
| `--json` | Structured JSON output |
| `--toon`, `-t` | Token-efficient structured output |

## MCP Parity

The same RPG features are available to MCP clients as:

- `grepai_rpg_search`
- `grepai_rpg_fetch`
- `grepai_rpg_explore`

The CLI keeps grepai's normal user-facing convention (`--json`, `--toon`, positional arguments), while MCP exposes structured tool parameters.
