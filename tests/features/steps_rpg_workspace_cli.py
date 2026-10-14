"""SEE-1411 gherkin steps — §SPEC-001/§SPEC-002/§SPEC-004 rpg workspace 通路。

以 subprocess 驱动真实 grepai CLI（fork 检出内构建），对实机
~/.grepai/workspace.yaml 与 postgres 后端断言：判据是端到端命令行为
（返回数据、干净报错、零写副作用），不做进程内 mock。
"""

from __future__ import annotations

import json
import os
import re
import subprocess
from datetime import datetime, timedelta
from pathlib import Path

from godot_qa_toolkit.gherkin.runner import Registry, StepFailure

_FORK_ROOT = Path(__file__).resolve().parents[2]
_BIN_CACHE: dict[str, str] = {}
STATE: dict[str, object] = {}


def register(registry: Registry) -> None:
    # ---- helpers ----------------------------------------------------------

    def _fail(reason: str, detail: str | None = None) -> None:
        raise StepFailure(reason=reason, detail=detail)

    def _build_binary() -> str:
        cached = _BIN_CACHE.get("bin")
        if cached:
            return cached
        override = os.environ.get("GREPAI_BIN")
        if override and Path(override).exists():
            _BIN_CACHE["bin"] = override
            return override
        out = _FORK_ROOT / "tests" / ".cache" / "grepai-see1411"
        if not out.exists():
            out.parent.mkdir(parents=True, exist_ok=True)
            env = os.environ.copy()
            env["PATH"] = "/usr/local/go/bin:" + env.get("PATH", "")
            subprocess.run(
                ["go", "build", "-o", str(out), "./cmd/grepai"],
                cwd=str(_FORK_ROOT), check=True, capture_output=True, text=True,
                env=env, timeout=600,
            )
        _BIN_CACHE["bin"] = str(out)
        return str(out)

    def _grepai(args: list[str], cwd: str | None = None) -> subprocess.CompletedProcess:
        return subprocess.run(
            [_build_binary(), *args],
            cwd=cwd, capture_output=True, text=True, timeout=180,
        )

    def _workspace_project_path(ws_default: str, proj_default: str) -> str:
        import yaml
        cfg_path = Path.home() / ".grepai" / "workspace.yaml"
        cfg = yaml.safe_load(cfg_path.read_text(encoding="utf-8"))
        ws = os.environ.get("GREPAI_WS_" + ws_default.upper(), ws_default)
        proj = os.environ.get("GREPAI_PROJECT_" + proj_default.upper(), proj_default)
        projects = cfg["workspaces"][ws]["projects"]
        if isinstance(projects, dict):
            return str(projects[proj]["path"])
        for p in projects or []:
            if p.get("name") == proj:
                return str(p["path"])
        _fail(f"project {proj!r} not found in workspace config {cfg_path}")

    def _recent_iso(ts: str, max_age: timedelta) -> bool:
        stamp = re.sub(r"\.\d+", "", ts)
        dt = datetime.fromisoformat(stamp)
        return dt >= datetime.now(dt.tzinfo) - max_age

    def _results_from(proc: subprocess.CompletedProcess) -> list[dict]:
        payload = json.loads(proc.stdout)
        if isinstance(payload, dict) and isinstance(payload.get("results"), list):
            return payload["results"]
        return list(payload)

    def _search(query: str, flags: list[str]) -> subprocess.CompletedProcess:
        proc = _grepai(["rpg", "search", query, *flags], cwd=_cwd())
        STATE["last_run"] = proc
        return proc

    def _cwd() -> str | None:
        return STATE.get("cwd")

    # ---- Given ------------------------------------------------------------

    @registry.step("a built grepai binary")
    def _given_binary(_ctx: dict) -> None:
        _build_binary()

    @registry.step("a built grepai binary with a known gherkin node id from search")
    def _given_known_node(_ctx: dict) -> None:
        proc = _search("gherkin", ["--workspace", "gqt", "--json"])
        if proc.returncode != 0:
            _fail("workspace search failed", detail=proc.stderr[:400])
        hits = [r["node"]["id"] for r in _results_from(proc)
                if "gherkin" in str(r["node"].get("path", ""))]
        if not hits:
            _fail("search produced no gherkin-path node to fetch")
        STATE["node_id"] = hits[0]

    @registry.step("the godot-qa-toolkit project cwd of the gqt workspace")
    def _given_gqt_cwd(_ctx: dict) -> None:
        STATE["cwd"] = _workspace_project_path("gqt", "godot-qa-toolkit")
        gob_files = sorted((Path(_cwd()) / ".grepai").glob("*.gob*"))
        STATE["gob_snapshot"] = {p.name: (p.stat().st_mtime_ns, p.stat().st_size)
                                 for p in gob_files}

    @registry.step("the king-of-likes project cwd of the kol workspace")
    def _given_kol_cwd(_ctx: dict) -> None:
        STATE["cwd"] = _workspace_project_path("kol", "king-of-likes")

    # ---- When -------------------------------------------------------------

    @registry.step('a bare rpg search runs with query "{query}" and --json')
    def _when_bare(_ctx: dict, query: str) -> None:
        _search(query, ["--json"])

    @registry.step('rpg search runs with query "gherkin" and --workspace gqt --json')
    def _when_workspace_search(_ctx: dict) -> None:
        _search("gherkin", ["--workspace", "gqt", "--json"])

    @registry.step('rpg search runs with query "anything" and --workspace ghost --json')
    def _when_ghost(_ctx: dict) -> None:
        _search("anything", ["--workspace", "ghost", "--json"])

    @registry.step('rpg search runs with query "anything" and --workspace gqt --project phantom --json')
    def _when_phantom(_ctx: dict) -> None:
        _search("anything", ["--workspace", "gqt", "--project", "phantom", "--json"])

    @registry.step('rpg search runs with query "anything" and --project gqt --json')
    def _when_project_workspaceless(_ctx: dict) -> None:
        _search("anything", ["--project", "gqt", "--json"])

    @registry.step("rpg fetch runs on that node id with --workspace gqt --json")
    def _when_fetch(_ctx: dict) -> None:
        proc = _grepai(["rpg", "fetch", str(STATE["node_id"]),
                        "--workspace", "gqt", "--json"], cwd=_cwd())
        STATE["last_run"] = proc

    @registry.step("rpg explore runs on that node id with --workspace gqt --json")
    def _when_explore(_ctx: dict) -> None:
        proc = _grepai(["rpg", "explore", str(STATE["node_id"]),
                        "--workspace", "gqt", "--json"], cwd=_cwd())
        STATE["last_run"] = proc

    # ---- Then -------------------------------------------------------------

    @registry.step('the command fails with error text mentioning "{fragment}"')
    def _then_error_mentions(_ctx: dict, fragment: str) -> None:
        proc = STATE.get("last_run")
        if proc is None:
            _fail("no command was run")
        if proc.returncode == 0:
            _fail("command unexpectedly succeeded (no clean error)",
                  detail=f"stdout={proc.stdout[:400]!r}")
        if fragment not in (proc.stdout + proc.stderr):
            _fail(f"error text does not mention {fragment!r}",
                  detail=f"stderr={proc.stderr[:400]!r}")

    @registry.step("the command fails with a --project-requires-workspace error")
    def _then_proj_flag_error(_ctx: dict) -> None:
        proc = STATE.get("last_run")
        if proc.returncode == 0:
            _fail("command unexpectedly succeeded (no clean error)")
        if "--project requires --workspace" not in (proc.stdout + proc.stderr):
            _fail("missing --project requires --workspace error",
                  detail=f"stderr={proc.stderr[:400]!r}")

    @registry.step("results are non-empty with no error")
    def _then_nonempty(_ctx: dict) -> None:
        proc = STATE["last_run"]
        if proc.returncode != 0:
            _fail("rpg search failed", detail=f"stderr={proc.stderr[:400]!r}")
        if len(_results_from(proc)) == 0:
            _fail("rpg search returned no results")

    @registry.step('at least one result node path contains "{fragment}"')
    def _then_hits(_ctx: dict, fragment: str) -> None:
        proc = STATE["last_run"]
        if proc.returncode != 0:
            _fail("rpg search failed", detail=f"stderr={proc.stderr[:400]!r}")
        hits = [r for r in _results_from(proc)
                if fragment in str(r["node"].get("path", ""))]
        if not hits:
            _fail(f"no result node path contains {fragment!r}",
                  detail=json.dumps(_results_from(proc)[:3], ensure_ascii=False)[:600])

    @registry.step("every returned node was updated within the last day")
    def _then_fresh(_ctx: dict) -> None:
        proc = STATE["last_run"]
        for r in _results_from(proc):
            ts = r["node"].get("updated_at")
            if not ts or not _recent_iso(ts, timedelta(days=1)):
                _fail(f"stale node {r['node'].get('id')!r}",
                      detail=f"updated_at={ts}")

    @registry.step("the fetched node id equals the query id")
    def _then_fetched_id(_ctx: dict) -> None:
        proc = STATE["last_run"]
        if proc.returncode != 0:
            _fail("rpg fetch failed", detail=f"stderr={proc.stderr[:400]!r}")
        payload = json.loads(proc.stdout)
        got = (payload.get("node") or {}).get("id")
        if got != STATE["node_id"]:
            _fail("fetched node id mismatch",
                  detail=f"{got} != {STATE['node_id']}")

    @registry.step("the fetch result carries parents and feature path")
    def _then_fetch_context(_ctx: dict) -> None:
        payload = json.loads(STATE["last_run"].stdout)
        if not payload.get("parents"):
            _fail("fetch result has no parents")
        if not payload.get("feature_path"):
            _fail("fetch result has no feature path")

    @registry.step("the explored graph contains the start node")
    def _then_start_present(_ctx: dict) -> None:
        payload = json.loads(STATE["last_run"].stdout)
        if STATE["node_id"] not in (payload.get("nodes") or {}):
            _fail("start node missing from explored graph")

    @registry.step("the explored graph has more than one node")
    def _then_graph_big(_ctx: dict) -> None:
        payload = json.loads(STATE["last_run"].stdout)
        if len(payload.get("nodes") or {}) <= 1:
            _fail("explored graph is trivial")

    @registry.step("no .grepai gob file under the project changed since the scenario start")
    def _then_zero_write(_ctx: dict) -> None:
        now = {p.name: (p.stat().st_mtime_ns, p.stat().st_size)
               for p in (Path(_cwd()) / ".grepai").glob("*.gob*")}
        before = STATE.get("gob_snapshot") or {}
        if now != before:
            _fail("read path wrote back gob files",
                  detail=f"before={before} after={now}")
