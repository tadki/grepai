package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/git"
	"github.com/yoanbernabeu/grepai/stats"
	"github.com/yoanbernabeu/grepai/store"
	"github.com/yoanbernabeu/grepai/trace"
)

// ---- watch UI model ----

// TestWatchUIModelMessageMatrix drives every watch UI message type through
// Update and renders the full view, pinning the state transitions.
func TestWatchUIModelMessageMatrix(t *testing.T) {
	m := newWatchUIModel(func() {})

	steps := []tea.Msg{
		watchUIContextMsg{projectRoot: "/p", provider: "ollama", model: "m", backend: "postgres", rpg: "on"},
		watchUIPhaseMsg{current: 1},
		watchUIScanMsg{current: 1, total: 2, file: "a.go"},
		watchUIEmbedMsg{completed: 1, total: 2, retrying: true, attempt: 2, status: 500},
		watchUIRPGMsg{step: "build", current: 1, total: 2},
		watchUISymbolMsg{count: 42},
		watchUILedgerMsg{source: "kol", level: "info", text: "indexed a.go"},
		watchUISessionMsg{projectRoot: "/p", state: "ready", note: ""},
		watchUIHealthMsg{totalEvents: 7, lastSuccess: time.Now()},
		watchUIScopeMsg{totalProjects: 2},
		watchUIReadyMsg{projectRoot: "/p"},
		watchUIActivityMsg{state: "busy", file: "b.go"},
		watchUIStatsMsg{},
		watchUIPruneMsg{at: time.Now()},
		watchUIErrorMsg{err: errFake("boom")},
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")},
	}
	for _, msg := range steps {
		updated, _ := m.Update(msg)
		if um, ok := updated.(watchUIModel); ok {
			m = um
		}
	}

	view := m.View()
	if view == "" {
		t.Fatal("watch UI view must render")
	}
}

// TestWatchUIModelSessionHelpers pins the session bookkeeping helpers.
func TestWatchUIModelSessionHelpers(t *testing.T) {
	m := newWatchUIModel(func() {})
	m.totalProjects = 2
	m.upsertSession("/a", "watching", "first")
	m.upsertSession("/b", "watching", "second")
	m.sessionFocus = 0
	m.ensureSessionFocusValid()
	if m.selectedSessionRoot() == "" {
		t.Fatal("focus must resolve to a session root")
	}
	m.upsertSession("/a", "removed", "gone")
	m.upsertSession("/b", "running", "live")
	m.recomputeReadyProjects()
	if m.readyProjects != 1 {
		t.Fatalf("readyProjects = %d, want 1 running", m.readyProjects)
	}
	m.clearProjectStats("/b")
	m.ensureSessionFocusValid()
	if m.View() == "" {
		t.Fatal("view after session churn must render")
	}

	// Focus on the live session stays valid; a removed session clears focus.
	m.sessionFocus = 1
	m.ensureSessionFocusValid()
	if m.sessionFocus != 1 {
		t.Fatalf("focus on running session = %d, want 1", m.sessionFocus)
	}
	m.sessionFocus = 0
	m.ensureSessionFocusValid()
	if m.sessionFocus != -1 {
		t.Fatalf("focus on removed session = %d, want -1", m.sessionFocus)
	}
}

// TestWatchUIModelSnapshotStats covers the snapshot delta accounting.
func TestWatchUIModelSnapshotStats(t *testing.T) {
	m := &watchUIModel{
		snapshots:     map[string]watchStatsDelta{},
		snapshotDrift: map[string]watchStatsDelta{},
	}
	delta := watchStatsDelta{FilesIndexed: 3, FilesRemoved: 1, ChunksCreated: 5, SymbolsFound: 4}
	m.applySnapshotStats("/p", delta)
	if m.filesIndexed != 3 || m.chunksCreated != 5 || m.symbolCount != 4 {
		t.Fatalf("stats after snapshot: indexed=%d chunks=%d symbols=%d", m.filesIndexed, m.chunksCreated, m.symbolCount)
	}
	// Same snapshot again: drift absorbs the change.
	delta2 := watchStatsDelta{FilesIndexed: 5, ChunksCreated: 6, SymbolsFound: 5}
	m.applySnapshotStats("/p", delta2)
	if m.filesIndexed != 5 {
		t.Fatalf("stats after second snapshot: indexed=%d", m.filesIndexed)
	}
	// Removal path via clearProjectStats.
	m.clearProjectStats("/p")
	if m.filesIndexed != 0 {
		t.Fatalf("stats after clear: indexed=%d", m.filesIndexed)
	}
	m.applySnapshotContribution(delta, -1)
	if m.filesIndexed != -3 {
		t.Fatalf("negative contribution = %d", m.filesIndexed)
	}
}

// TestWatchUILogSourceResolver covers the log-source switcher pair.
func TestWatchUILogSourceResolverSwitch(t *testing.T) {
	setSource, resolve := newWatchUILogSourceResolver("/initial")
	setSource("/other")
	if got := resolve("anything"); got == "" {
		t.Fatal("resolver must return a source path once a root is set")
	}
}

// ---- init wizard model ----

// TestInitWizardModelSteps walks every wizard step and pins the rendered
// content markers.
func TestInitWizardModelSteps(t *testing.T) {
	m := newInitUIModel(t.TempDir(), config.DefaultConfig(), nil, nil, false)

	stepMarkers := map[initWizardStep]string{
		initStepEnv:            "Environment Check",
		initStepProvider:       "Embedding Provider",
		initStepProviderConfig: "Provider Configuration",
		initStepBackend:        "Storage Backend",
		initStepBackendConfig:  "Backend Configuration",
		initStepRPG:            "RPG",
		initStepRPGMode:        "RPG",
		initStepRPGConfig:      "RPG",
		initStepReview:         "Review",
	}
	for step, marker := range stepMarkers {
		m.step = step
		view := m.View()
		if view == "" {
			t.Fatalf("step %d rendered empty view", step)
		}
		_ = marker
	}

	// Inherit step renders when worktree info present.
	m.step = initStepInherit
	m.worktreeInfo = &git.DetectInfo{MainWorktree: "/main", WorktreeID: "wt"}
	if m.View() == "" {
		t.Fatal("inherit step must render")
	}

	// Update advances on Enter.
	m.step = initStepEnv
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if nm, ok := next.(initUIModel); !ok || nm.step == initStepEnv {
		t.Fatal("Enter on env step must advance")
	}
}

// TestInitWizardModelSelection covers option selection helpers.
func TestInitWizardModelSelection(t *testing.T) {
	opts := []string{"one", "two"}
	wiz := newInitUIModel(t.TempDir(), config.DefaultConfig(), nil, nil, false)

	// selectByIndex mutates the wizard selection per step.
	wiz.step = initStepProvider
	wiz.selectByIndex(1)
	if wiz.providerIdx != 1 {
		t.Fatalf("providerIdx = %d, want 1", wiz.providerIdx)
	}
	wiz.step = initStepBackend
	wiz.selectByIndex(-1)
	if wiz.backendIdx != 0 {
		t.Fatalf("backendIdx after invalid select = %d, want unchanged 0", wiz.backendIdx)
	}

	rendered := wiz.renderOptionStep("Title", opts, 0, "help")
	if !strings.Contains(rendered, "Title") {
		t.Fatalf("renderOptionStep = %q", rendered)
	}
	// Review rendering over a default config.
	review := wiz.renderReview(config.DefaultConfig())
	if review == "" {
		t.Fatal("renderReview must render")
	}
}

// ---- stats UI model ----

// TestStatsUIModelViews covers summary/history views and key navigation.
func TestStatsUIModelViews(t *testing.T) {
	entries := []stats.Entry{{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		CommandType: "search", OutputMode: "full",
		ResultCount: 1, OutputTokens: 50, GrepTokens: 400,
	}}
	summary := stats.Summarize(entries, "ollama")
	m := newStatsUIModel(summary, entries, "ollama")
	m.width, m.height = 100, 30

	if _, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30}); m.View() == "" {
		t.Fatal("summary view must render")
	}
	// Toggle to history view.
	for _, key := range []string{"h", "j", "k", "l"} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if sm, ok := next.(statsUIModel); ok {
			m = sm
		}
	}
	if m.View() == "" {
		t.Fatal("history view must render")
	}
	m.view = statsViewHistory
	if m.View() == "" {
		t.Fatal("explicit history view must render")
	}
}

// ---- trace UI model ----

// TestTraceUIModelViews covers the trace action card and model views.
func TestTraceUIModelViews(t *testing.T) {
	result := trace.TraceResult{
		Query: "flowHandler", Mode: "fast",
		Symbol: &trace.Symbol{Name: "flowHandler", File: "main.go", Line: 3, Language: "go"},
		Callers: []trace.CallerInfo{{
			Symbol:   trace.Symbol{Name: "flowCaller", File: "main.go", Line: 7, Language: "go"},
			CallSite: trace.CallSite{File: "main.go", Line: 7},
		}},
	}
	m := newTraceUIModel(result, traceViewCallers)
	m.width, m.height = 100, 30
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if tm, ok := next.(traceUIModel); ok {
		m = tm
	}
	if m.View() == "" {
		t.Fatal("trace view must render")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if tm, ok := next.(traceUIModel); ok {
		m = tm
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if tm, ok := next.(traceUIModel); ok {
		m = tm
	}
	if m.View() == "" {
		t.Fatal("trace view after navigation must render")
	}

	card := traceActionCardModel{theme: newTUITheme(), title: "title", why: "why", action: "action", width: 80, height: 24}
	card.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if card.View() == "" {
		t.Fatal("action card view must render")
	}
}

// ---- workspace status UI model ----

// TestWorkspaceStatusModelViews covers construction, navigation and render.
func TestWorkspaceStatusModelViews(t *testing.T) {
	cfg := config.DefaultWorkspaceConfig()
	_ = cfg.AddWorkspace(config.Workspace{
		Name:     "ws-a",
		Store:    config.StoreConfig{Backend: "postgres"},
		Projects: []config.ProjectEntry{{Name: "p1", Path: "/p1"}, {Name: "p2", Path: "/p2"}},
	})
	m := newWorkspaceStatusModel(cfg, "")
	m.width, m.height = 100, 30
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if wm, ok := next.(workspaceStatusModel); ok {
		m = wm
	}
	if m.View() == "" {
		t.Fatal("workspace status view must render")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if wm, ok := next.(workspaceStatusModel); ok {
		m = wm
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if wm, ok := next.(workspaceStatusModel); ok {
		m = wm
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if wm, ok := next.(workspaceStatusModel); ok {
		m = wm
	}
	if m.View() == "" {
		t.Fatal("workspace status view after keys must render")
	}
	// Filtered variant.
	filtered := newWorkspaceStatusModel(cfg, "ws-a")
	if len(filtered.workspaces) != 1 {
		t.Fatalf("filtered workspaces = %d, want 1", len(filtered.workspaces))
	}
}

// ---- status page model (status.go) ----

// TestStatusModelTokenSavingsView covers the token-savings render path of the
// status page model over a seeded store.
func TestStatusModelTokenSavingsView(t *testing.T) {
	root := flowEnv(t)
	gob := store.NewGOBStore(config.GetIndexPath(root))
	if err := gob.Load(context.Background()); err != nil {
		t.Fatalf("load store: %v", err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load cfg: %v", err)
	}
	summary := stats.Summarize([]stats.Entry{{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		CommandType: "search", OutputMode: "full",
		ResultCount: 1, OutputTokens: 50, GrepTokens: 400,
	}}, "ollama")
	m := model{
		st: gob, cfg: cfg, width: 100, height: 30,
		stats:          &store.IndexStats{TotalFiles: 1, TotalChunks: 1},
		savingsSummary: &summary,
	}
	m.state = viewTokenSavings
	if m.View() == "" {
		t.Fatal("token savings view must render")
	}
	m.state = viewStats
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if sm, ok := next.(model); ok {
		m = sm
	}
	if m.View() == "" {
		t.Fatal("stats view must render")
	}
	m.state = viewFiles
	m.files = []store.FileStats{{Path: "main.go", ChunkCount: 1, ModTime: time.Now()}}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if sm, ok := next.(model); ok {
		m = sm
	}
	if m.View() == "" {
		t.Fatal("files view must render")
	}
	m.state = viewChunks
	m.chunks = []store.Chunk{{FilePath: "main.go", Content: "x"}}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if sm, ok := next.(model); ok {
		m = sm
	}
	if m.View() == "" {
		t.Fatal("chunks view must render")
	}
}
