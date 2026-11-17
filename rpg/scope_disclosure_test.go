package rpg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yoanbernabeu/grepai/config"
)

// TestRPGImplicitProjectDisclosure pins that a workspace RPG load without an
// explicit project returns a selection marked implicit (with the chosen
// workspace/project filled in), while an explicit project is marked
// non-implicit. MCP responses and CLI output disclose the implicit case so
// agents never mistake one project's graph for a workspace-wide view.
func TestRPGImplicitProjectDisclosure(t *testing.T) {
	home := t.TempDir()
	scopeTestEnv(t, home)
	base := t.TempDir()
	emptyRoot := filepath.Join(base, "proj-empty")
	dataRoot := seedRPGProject(t, base, "proj-data", "disclosureSolver")

	writeWorkspaceConfig(t, config.Workspace{
		Name: "ws-disclose",
		Projects: []config.ProjectEntry{
			{Name: "empty", Path: emptyRoot},
			{Name: "data", Path: dataRoot},
		},
	})

	// Implicit pick: no project named, multi-project workspace.
	store, qe, sel, err := LoadWorkspaceRPG(context.Background(), "ws-disclose", "")
	if err != nil {
		t.Fatalf("LoadWorkspaceRPG implicit: %v", err)
	}
	defer store.Close()
	if sel == nil {
		t.Fatal("expected non-nil scope selection")
	}
	if !sel.Implicit {
		t.Fatalf("selection.Implicit = false, want true for omitted project")
	}
	if sel.Workspace != "ws-disclose" {
		t.Fatalf("selection.Workspace = %q, want ws-disclose", sel.Workspace)
	}
	if sel.Project != "data" {
		t.Fatalf("selection.Project = %q, want data (first non-empty RPG index)", sel.Project)
	}
	res, err := qe.SearchNode(context.Background(), SearchNodeRequest{Query: "seeded feature disclosureSolver", Limit: 5})
	if err != nil || len(res) == 0 {
		t.Fatalf("expected implicit selection to serve the data project, got %d results err=%v", len(res), err)
	}

	// Explicit pick: naming the project must not be flagged implicit.
	store2, _, sel2, err := LoadWorkspaceRPG(context.Background(), "ws-disclose", "data")
	if err != nil {
		t.Fatalf("LoadWorkspaceRPG explicit: %v", err)
	}
	defer store2.Close()
	if sel2 == nil {
		t.Fatal("expected non-nil scope selection for explicit project")
	}
	if sel2.Implicit {
		t.Fatalf("selection.Implicit = true for explicitly named project %q", sel2.Project)
	}
	if sel2.Project != "data" || sel2.Workspace != "ws-disclose" {
		t.Fatalf("explicit selection = %s/%s, want ws-disclose/data", sel2.Workspace, sel2.Project)
	}
}
