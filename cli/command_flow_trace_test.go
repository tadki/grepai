package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestTraceCommandFlows drives callers/callees/graph over the seeded symbol
// store, including the JSON/compact contract and the flag-validation errors.
func TestTraceCommandFlows(t *testing.T) {
	flowEnv(t)
	reset := func() {
		traceMode, traceJSON, traceTOON, traceCompact, traceUI = "fast", false, false, false, false
		traceWorkspace, traceProject, traceDepth = "", "", 2
	}
	reset()
	resetFlowFlags(t, reset)

	callersOut, err := captureFlowStdout(t, func() error { return runTraceCallers(&cobra.Command{}, []string{"flowHandler"}) })
	if err != nil {
		t.Fatalf("trace callers: %v", err)
	}
	if !strings.Contains(callersOut, "flowCaller") {
		t.Fatalf("callers output %q lacks the caller symbol", callersOut)
	}
	calleesOut, err := captureFlowStdout(t, func() error { return runTraceCallees(&cobra.Command{}, []string{"flowHandler"}) })
	if err != nil {
		t.Fatalf("trace callees: %v", err)
	}
	if !strings.Contains(calleesOut, "flowHandler") {
		t.Fatalf("callees output %q lacks the queried symbol", calleesOut)
	}

	traceJSON = true
	out, err := captureFlowStdout(t, func() error { return runTraceCallers(&cobra.Command{}, []string{"flowHandler"}) })
	if err != nil || !strings.Contains(out, `"symbol"`) {
		t.Fatalf("trace json out=%q err=%v", out, err)
	}
	traceCompact = true
	if _, err := captureFlowStdout(t, func() error { return runTraceCallees(&cobra.Command{}, []string{"flowHandler"}) }); err != nil {
		t.Fatalf("trace compact json: %v", err)
	}

	// Graph traversal
	traceJSON, traceCompact = false, false
	out, err = captureFlowStdout(t, func() error { return runTraceGraph(&cobra.Command{}, []string{"flowHandler"}) })
	if err != nil {
		t.Fatalf("trace graph: %v", err)
	}

	// Unknown symbol reports empty callers without failing.
	ghostOut, ghostErr := captureFlowStdout(t, func() error { return runTraceCallers(&cobra.Command{}, []string{"ghostSymbol"}) })
	if ghostErr != nil {
		t.Fatalf("unknown-symbol callers must not error: %v", ghostErr)
	}
	if !strings.Contains(ghostOut, "No symbol found") {
		t.Fatalf("unknown-symbol output %q lacks the empty-callers notice", ghostOut)
	}

	// Flag validation: project without workspace
	traceProject = "p"
	if _, err := captureFlowStdout(t, func() error { return runTraceCallers(&cobra.Command{}, []string{"flowHandler"}) }); err == nil || !strings.Contains(err.Error(), "--project requires --workspace") {
		t.Fatalf("project-without-workspace err=%v", err)
	}
	traceProject = ""

	// Compact without json/toon
	traceCompact, traceJSON = true, false
	if _, err := captureFlowStdout(t, func() error { return runTraceCallers(&cobra.Command{}, []string{"flowHandler"}) }); err == nil || !strings.Contains(err.Error(), "--compact") {
		t.Fatalf("compact-without-json err=%v", err)
	}
	traceCompact = false

	// Workspace mode with unknown workspace fails cleanly.
	traceWorkspace = "ghost-ws"
	if _, err := captureFlowStdout(t, func() error { return runTraceCallers(&cobra.Command{}, []string{"flowHandler"}) }); err == nil {
		t.Fatal("workspace trace with unknown workspace must fail")
	}
}

// TestRefsCommandFlows exercises readers/writers/graph through the cobra
// commands, covering JSON output and the workspace flag errors.
func TestRefsCommandFlows(t *testing.T) {
	flowEnv(t)
	reset := func() {
		refsJSON, refsTOON, refsCompact = false, false, false
		refsWorkspace, refsProject = "", ""
	}
	reset()
	resetFlowFlags(t, reset)

	out, err := captureFlowStdout(t, func() error {
		result, rerr := runRefs("flowHandler", true)
		if rerr != nil {
			return rerr
		}
		return outputRefsResult(result, true)
	})
	if err != nil {
		t.Fatalf("refs readers: %v", err)
	}
	if !strings.Contains(out, "flowHandler") {
		t.Fatalf("readers output %q lacks the symbol", out)
	}

	refsJSON = true
	out, err = captureFlowStdout(t, func() error {
		result, rerr := runRefs("flowHandler", false)
		if rerr != nil {
			return rerr
		}
		return outputRefsResult(result, false)
	})
	if err != nil || !strings.Contains(out, `"query"`) {
		t.Fatalf("refs json out=%q err=%v", out, err)
	}

	refsCompact = true
	if _, err := captureFlowStdout(t, func() error {
		result, rerr := runRefs("flowHandler", false)
		if rerr != nil {
			return rerr
		}
		return outputRefsResult(result, false)
	}); err != nil {
		t.Fatalf("refs compact json: %v", err)
	}

	refsJSON = false
	if err := validateRefsOutputFlags(); err == nil || !strings.Contains(err.Error(), "--compact") {
		t.Fatalf("compact-without-json err=%v", err)
	}
	refsJSON = true

	readersResult, rerr := runRefs("flowHandler", true)
	if rerr != nil {
		t.Fatalf("refs graph readers: %v", rerr)
	}
	writersResult, rerr := runRefs("flowHandler", false)
	if rerr != nil {
		t.Fatalf("refs graph writers: %v", rerr)
	}
	graph := refsGraphResult{Query: "flowHandler", Kind: "property", Mode: "fast", Readers: readersResult.Readers, Writers: writersResult.Writers}
	if _, err := captureFlowStdout(t, func() error { return outputRefsGraphResult(graph) }); err != nil {
		t.Fatalf("refs graph output: %v", err)
	}

	refsCompact = false
	refsWorkspace, refsProject = "ghost-ws", ""
	if _, rerr := runRefs("flowHandler", true); rerr == nil {
		t.Fatal("workspace refs with unknown workspace must fail")
	}
	refsWorkspace, refsProject = "", "p"
	if _, rerr := runRefs("flowHandler", true); rerr == nil || !strings.Contains(rerr.Error(), "--project requires --workspace") {
		t.Fatalf("project-without-workspace err=%v", rerr)
	}
}

// TestRPGCommandFlows covers rpg search/fetch/explore against a seeded local
// gob graph plus the workspace/project validation errors.
func TestRPGCommandFlows(t *testing.T) {
	flowEnv(t)
	reset := func() {
		rpgJSON, rpgTOON = false, false
		rpgWorkspace, rpgProject = "", ""
		rpgSearchScope, rpgSearchKinds, rpgSearchLimit = "", "", 10
		rpgExploreDirection, rpgExploreDepth, rpgExploreEdgeTypes, rpgExploreLimit = "both", 2, "", 100
	}
	reset()
	resetFlowFlags(t, reset)

	// Local gob graph: seed via the same helpers the mcp tests use.
	seedLocalRPGGraph(t)

	rpgJSON = true
	out, err := captureFlowStdout(t, func() error { return runRPGSearch(&cobra.Command{}, []string{"seeded"}) })
	if err != nil {
		t.Fatalf("rpg search: %v", err)
	}
	if !strings.Contains(out, "node") {
		t.Fatalf("rpg search json out=%q lacks nodes", out)
	}

	fetchOut, err := captureFlowStdout(t, func() error { return runRPGFetch(&cobra.Command{}, []string{rpgSeedNodeID}) })
	if err != nil {
		t.Fatalf("rpg fetch: %v", err)
	}
	if !strings.Contains(fetchOut, rpgSeedNodeID) {
		t.Fatalf("rpg fetch out=%q lacks the node id", fetchOut)
	}

	if _, err := captureFlowStdout(t, func() error { return runRPGExplore(&cobra.Command{}, []string{rpgSeedNodeID}) }); err != nil {
		t.Fatalf("rpg explore: %v", err)
	}

	// --project without --workspace
	rpgProject = "p"
	if _, err := captureFlowStdout(t, func() error { return runRPGSearch(&cobra.Command{}, []string{"q"}) }); err == nil || !strings.Contains(err.Error(), "--project requires --workspace") {
		t.Fatalf("project-without-workspace err=%v", err)
	}

	// Ghost node fetch errors.
	rpgProject = ""
	if _, err := captureFlowStdout(t, func() error { return runRPGFetch(&cobra.Command{}, []string{"sym:ghost:x"}) }); err == nil {
		t.Fatal("ghost node fetch must error")
	}

	// Invalid explore direction.
	rpgExploreDirection = "sideways"
	if _, err := captureFlowStdout(t, func() error { return runRPGExplore(&cobra.Command{}, []string{rpgSeedNodeID}) }); err == nil || !strings.Contains(err.Error(), "direction") {
		t.Fatalf("invalid direction err=%v", err)
	}
}

// TestCompletionCommandFlows generates each shell completion script.
func TestCompletionCommandFlows(t *testing.T) {
	for _, shell := range []string{"zsh", "bash", "fish", "powershell"} {
		out, err := captureFlowStdout(t, func() error {
			rootCmd.SetArgs([]string{"completion", shell})
			return rootCmd.Execute()
		})
		if err != nil {
			t.Fatalf("completion %s: %v", shell, err)
		}
		if len(out) < 100 {
			t.Fatalf("completion %s script suspiciously short (%d bytes)", shell, len(out))
		}
	}
}
