#!/usr/bin/env bash
# SEE-1411 §SPEC-003 — single-project (gob) RPG flow regression script.
# Builds the CLI from the checkout, creates a throwaway gob project with RPG
# enabled, indexes it with a background watch run, exercises rpg
# search/fetch/explore, and asserts the read path writes nothing back.
# Requires a reachable ollama at $GREPAI_OLLAMA.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${GREPAI_BIN:-$REPO_ROOT/tests/.cache/grepai-e2e}"

OLLAMA="${GREPAI_OLLAMA:-http://192.168.1.210:11434}"
PROJECT="$(mktemp -d "${TMPDIR:-/tmp}/grepai-rpg-e2e.XXXXXX")"

# Mid-failure exits (set -e, or the labeled failures below) must not leak the
# throwaway watcher or its project dir; the EXIT trap reaps both.
WATCH_STARTED=0
cleanup() {
  if [ "$WATCH_STARTED" = 1 ]; then
    (cd "$PROJECT" && "$BIN" watch --stop >/dev/null 2>&1 || true)
  fi
  rm -rf "$PROJECT"
}
trap cleanup EXIT

echo "[1/5] Build"
if [ ! -x "$BIN" ]; then
  mkdir -p "$(dirname "$BIN")"
  (cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/grepai)
fi

echo "[2/5] Init gob project at $PROJECT"
mkdir -p "$PROJECT/cmd"
cat > "$PROJECT/cmd/search.go" <<'EOF'
package cmd

import "fmt"

func runSearch(query string) error {
	fmt.Println("searching", query)
	return nil
}
EOF
mkdir -p "$PROJECT/.grepai"
(cd "$PROJECT" && "$BIN" init -b gob -p ollama -m nomic-embed-text --yes >/dev/null)
# Points the embedder and the RPG LLM extractor at the local ollama host and
# enables RPG for the throwaway project.
sed -i "s|http://localhost:11434|$OLLAMA|g; s|http://localhost:11434/v1|$OLLAMA/v1|g" "$PROJECT/.grepai/config.yaml"
awk 'prev=="rpg:" {gsub(/    enabled: false/, "    enabled: true")} {print; prev=$0}' "$PROJECT/.grepai/config.yaml" > "$PROJECT/.grepai/config.yaml.new"
mv "$PROJECT/.grepai/config.yaml.new" "$PROJECT/.grepai/config.yaml"
grep -A1 '^rpg:' "$PROJECT/.grepai/config.yaml" | grep -q 'enabled: true'

echo "[3/5] Index via background watch"
(cd "$PROJECT" && "$BIN" watch --background --log-dir "$PROJECT/.grepai/logs" >/dev/null)
WATCH_STARTED=1
for _ in $(seq 1 120); do
  if (cd "$PROJECT" && "$BIN" rpg search "runSearch" --json >/dev/null 2>&1); then
    break
  fi
  sleep 2
done

echo "[4/5] Query RPG"
out="$(cd "$PROJECT" && "$BIN" rpg search "runSearch" --json 2>&1)"
count="$(echo "$out" | grep -o '"id"' | wc -l)"
[ "$count" -gt 0 ] || { echo "search returned no nodes: $out"; exit 4; }
node_id="$(echo "$out" | sed -n 's/.*"id": "\([^"]*\)".*/\1/p' | head -1)"
(cd "$PROJECT" && "$BIN" rpg fetch "$node_id" >/dev/null)
(cd "$PROJECT" && "$BIN" rpg explore "$node_id" --direction forward >/dev/null)

echo "[5/5] Zero-write check"
m1="$(stat -c '%Y' "$PROJECT/.grepai/rpg.gob")"
(cd "$PROJECT" && "$BIN" rpg search "runSearch" >/dev/null)
m2="$(stat -c '%Y' "$PROJECT/.grepai/rpg.gob")"
[ "$m1" = "$m2" ] || { echo "read path rewrote rpg.gob"; exit 5; }

# The throwaway watcher and project dir are reaped by the EXIT trap.

echo "PASS: single-project RPG flow (search=$count results, fetch/explore ok, zero-write)"
