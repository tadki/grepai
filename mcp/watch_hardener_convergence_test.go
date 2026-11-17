package mcp

import "testing"

// Added by the SEE-1597 P1 hardener: pins the exact discovery-hint wording
// including the "first" guidance so agents are told to list workspaces before
// guessing names (kills mutant D11-hint-first-dropped from
// see1597-p1-hardener.json on the issue).
func TestWorkspaceDiscoveryHintPinsFirstGuidance(t *testing.T) {
	got := workspaceDiscoveryHint([]string{"zeta", "alpha"})
	want := "Valid workspaces: alpha, zeta — call grepai_list_workspaces first to discover them"
	if got != want {
		t.Fatalf("hint = %q, want %q", got, want)
	}
}
