package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func captureFlowStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	var out string
	var err error
	out = captureStdout(t, func() {
		err = fn()
	})
	return out, err
}

// TestSearchCommandFlows drives the search command end to end through the
// stubbed embedder and seeded GOB index: default table output, JSON, compact
// JSON, path filtering and both flag-validation error branches.
func TestSearchCommandFlows(t *testing.T) {
	projectRoot := flowEnv(t)
	_ = projectRoot

	reset := func() {
		searchLimit, searchJSON, searchTOON, searchCompact = 10, false, false, false
		searchWorkspace, searchProjects, searchPath = "", nil, ""
	}
	reset()
	resetFlowFlags(t, reset)

	// Default output
	out, err := captureFlowStdout(t, func() error {
		return runSearch(&cobra.Command{}, []string{"flowHandler"})
	})
	if err != nil {
		t.Fatalf("default search: %v", err)
	}
	if !strings.Contains(out, "main.go") {
		t.Fatalf("default search output %q lacks the hit file", out)
	}

	// JSON output
	searchJSON = true
	out, err = captureFlowStdout(t, func() error {
		return runSearch(&cobra.Command{}, []string{"flowHandler"})
	})
	if err != nil || !strings.Contains(out, `"file_path": "main.go"`) {
		t.Fatalf("json search out=%q err=%v", out, err)
	}

	// Compact JSON omits content
	searchCompact = true
	out, err = captureFlowStdout(t, func() error {
		return runSearch(&cobra.Command{}, []string{"flowHandler"})
	})
	if err != nil {
		t.Fatalf("compact json search: %v", err)
	}
	if strings.Contains(out, "func flowHandler() {}") {
		t.Fatalf("compact output must omit content: %q", out)
	}

	// Path filter that matches
	searchPath = "main.go"
	if _, err := captureFlowStdout(t, func() error {
		return runSearch(&cobra.Command{}, []string{"flowHandler"})
	}); err != nil {
		t.Fatalf("path-filtered search: %v", err)
	}

	// Path filter that matches nothing
	searchPath = "nonexistent/"
	_, err = captureFlowStdout(t, func() error {
		return runSearch(&cobra.Command{}, []string{"flowHandler"})
	})
	if err != nil {
		t.Fatalf("empty result search must not error: %v", err)
	}

	// compact without json/toon must fail
	searchJSON, searchCompact, searchTOON = false, true, false
	if _, err := captureFlowStdout(t, func() error {
		return runSearch(&cobra.Command{}, []string{"q"})
	}); err == nil || !strings.Contains(err.Error(), "--compact flag requires") {
		t.Fatalf("compact-without-json err=%v", err)
	}

	// --project without --workspace must fail
	searchCompact, searchProjects, searchWorkspace = false, []string{"p"}, ""
	if _, err := captureFlowStdout(t, func() error {
		return runSearch(&cobra.Command{}, []string{"q"})
	}); err == nil || !strings.Contains(err.Error(), "--project flag requires --workspace") {
		t.Fatalf("project-without-workspace err=%v", err)
	}

	// Workspace mode with an unknown workspace surfaces the config error.
	searchWorkspace = "ghost-ws"
	if _, err := captureFlowStdout(t, func() error {
		return runSearch(&cobra.Command{}, []string{"q"})
	}); err == nil {
		t.Fatal("workspace search with unknown workspace must fail")
	}
}

// TestStatsCommandFlows covers the stats recorder and the JSON/table/history
// render paths of the stats command.
func TestStatsCommandFlows(t *testing.T) {
	projectRoot := flowEnv(t)
	reset := func() {
		statsJSON, statsHistory, statsNoUI = false, false, false
		statsLimit = 30
	}
	reset()
	resetFlowFlags(t, reset)

	recordSearchStats(projectRoot, "search", "full", 3, "output")

	out, err := captureFlowStdout(t, func() error {
		return runStats(&cobra.Command{}, nil)
	})
	if err != nil {
		t.Fatalf("stats table: %v", err)
	}
	if out == "" {
		t.Fatal("stats table output must not be empty")
	}

	statsJSON = true
	out, err = captureFlowStdout(t, func() error {
		return runStats(&cobra.Command{}, nil)
	})
	if err != nil || !strings.Contains(out, "total_queries") {
		t.Fatalf("stats json out=%q err=%v", out, err)
	}

	statsHistory = true
	statsLimit = 5
	if _, err := captureFlowStdout(t, func() error {
		return runStats(&cobra.Command{}, nil)
	}); err != nil {
		t.Fatalf("stats history: %v", err)
	}
}

// TestStatusCommandFlows drives the plain-text status rendering over the
// seeded GOB index.
func TestStatusCommandFlows(t *testing.T) {
	flowEnv(t)
	reset := func() { statusNoUI = false }
	reset()
	resetFlowFlags(t, reset)

	statusNoUI = true
	out, err := captureFlowStdout(t, func() error {
		return runStatus(&cobra.Command{}, nil)
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if out == "" {
		t.Fatal("status output must not be empty")
	}
}
