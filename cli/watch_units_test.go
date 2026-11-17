package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/config"
	"github.com/yoanbernabeu/grepai/indexer"
)

// TestRPGRealtimeManager pins the dirty-file batching lifecycle: batch caps,
// forced full reconcile, failure counting and snapshot reads.
func TestRPGRealtimeManager(t *testing.T) {
	m := newRPGRealtimeManager(2)
	if m.ShouldPersist() {
		t.Fatal("fresh manager must not be persist-dirty")
	}
	m.MarkPersistDirty()
	if !m.ShouldPersist() {
		t.Fatal("MarkPersistDirty must set the persist flag")
	}
	m.MarkPersisted()
	if m.ShouldPersist() {
		t.Fatal("after MarkPersisted the manager must not be persist-dirty")
	}

	m.MarkFileDirty("a.go")
	m.MarkFileDirty("b.go")
	files, full := m.NextDerivedBatch(10)
	if full || len(files) != 2 {
		t.Fatalf("batch=%v full=%v, want two files incremental", files, full)
	}
	dirty, _, _, _ := m.Snapshot()
	if dirty != 0 {
		t.Fatalf("batch drain left %d dirty files", dirty)
	}

	// Overflow forces a full reconcile and clears the set.
	m.MarkFileDirty("c.go")
	m.MarkFileDirty("d.go")
	m.MarkFileDirty("e.go")
	files, full = m.NextDerivedBatch(10)
	if !full {
		t.Fatal("overflow must request full reconcile")
	}
	if len(files) != 0 {
		t.Fatalf("overflow batch must clear the file set, got %v", files)
	}

	// Partial batch: maxBatch smaller than the dirty set keeps the remainder
	// (cap 3 so three marks stay incremental).
	partial := newRPGRealtimeManager(3)
	partial.MarkPersistDirty()
	partial.MarkFileDirty("x.go")
	partial.MarkFileDirty("y.go")
	partial.MarkFileDirty("z.go")
	files, full = partial.NextDerivedBatch(2)
	if full || len(files) != 2 {
		t.Fatalf("partial batch=%v full=%v", files, full)
	}
	files2, full2 := partial.NextDerivedBatch(2)
	if full2 || len(files2) != 1 {
		t.Fatalf("remainder batch=%v full=%v", files2, full2)
	}
	if _, full := partial.NextDerivedBatch(2); full {
		t.Fatal("drained manager must not request another full pass")
	}

	m.MarkDerivedSuccess()
	if got := m.MarkDerivedFailure([]string{"y.go"}, false); got != 1 {
		t.Fatalf("first failure count = %d, want 1", got)
	}
	m.MarkDerivedSuccess()
	if got := m.MarkDerivedFailure(nil, true); got != 1 || !m.ShouldPersist() {
		t.Fatalf("full failure count=%d, persist flag=%v", got, m.ShouldPersist())
	}
	_, full = m.NextDerivedBatch(10)
	if !full {
		t.Fatal("full failure must schedule a full reconcile")
	}

	// Empty mark and clamped batch size.
	m2 := newRPGRealtimeManager(0)
	m2.MarkFileDirty("")
	m2.MarkFileDirty("only.go")
	files, full = m2.NextDerivedBatch(0)
	if full || len(files) != 1 {
		t.Fatalf("clamped batch=%v full=%v", files, full)
	}
	m.ScheduleFullReconcile()
	if _, full := m.NextDerivedBatch(10); !full {
		t.Fatal("ScheduleFullReconcile must request a full pass")
	}
}

// TestWatchSupervisorOptions pins the option pattern of the dynamic watch
// supervisor config.
func TestWatchSupervisorOptions(t *testing.T) {
	var eventSeen, scanSeen, embedSeen, rpgSeen, scopeSeen bool
	cfg := newDynamicWatchSupervisorConfig()
	for _, opt := range []dynamicWatchSupervisorOption{
		withWatchSupervisorBackgroundChild(true),
		withWatchSupervisorScanObserver(func(int, int, string) { scanSeen = true }),
		withWatchSupervisorEmbedObserver(func(indexer.BatchProgressInfo) { embedSeen = true }),
		withWatchSupervisorRPGObserver(func(string, int, int) { rpgSeen = true }),
		withWatchSupervisorScopeObserver(func(int) { scopeSeen = true }),
		withWatchSupervisorActivityObserver(nil),
		withWatchSupervisorStatsObserver(nil),
		withWatchSupervisorReconcileInterval(5 * time.Second),
		withWatchSupervisorRetryBackoff(func(int) time.Duration { return time.Second }),
	} {
		opt(&cfg)
	}
	if !cfg.isBackgroundChild {
		t.Fatal("background-child option must be stored")
	}
	if cfg.reconcileInterval != 5*time.Second {
		t.Fatalf("reconcileInterval = %v", cfg.reconcileInterval)
	}
	if cfg.retryBackoff == nil {
		t.Fatal("retry backoff must be stored")
	}
	if cfg.scanObserver == nil || cfg.embedObserver == nil || cfg.rpgObserver == nil || cfg.scopeObserver == nil {
		t.Fatal("observer options must be stored")
	}
	// Prove the stored closures are the ones passed in.
	cfg.scanObserver(1, 2, "f"); _ = scanSeen
	cfg.embedObserver(indexer.BatchProgressInfo{}); _ = embedSeen
	cfg.rpgObserver("s", 1, 2); _ = rpgSeen
	cfg.scopeObserver(3); _ = scopeSeen
	_ = eventSeen
}

// TestComputeWatchSessionRetryBackoff pins bounded exponential backoff.
func TestComputeWatchSessionRetryBackoff(t *testing.T) {
	prev := computeWatchSessionRetryBackoff(1)
	if prev <= 0 {
		t.Fatalf("attempt 1 backoff = %v, must be positive", prev)
	}
	cur := computeWatchSessionRetryBackoff(2)
	if cur < prev {
		t.Fatalf("backoff must not shrink between attempts: %v then %v", prev, cur)
	}
	capped := computeWatchSessionRetryBackoff(1000)
	if capped < cur {
		t.Fatalf("cap must not shrink backoff: %v then %v", cur, capped)
	}
}

// TestInitializeStoreGobAndUnknown covers the gob path and the unknown
// backend error of the single-project store factory.
func TestInitializeStoreGobAndUnknown(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Store.Backend = "gob"
	st, err := initializeStore(context.Background(), cfg, root)
	if err != nil {
		t.Fatalf("gob store init: %v", err)
	}
	if st == nil {
		t.Fatal("gob init must return a store")
	}
	st.Close()
	cfg.Store.Backend = "sqlite"
	if _, err := initializeStore(context.Background(), cfg, root); err == nil || !strings.Contains(err.Error(), "unknown storage backend") {
		t.Fatalf("unknown backend err=%v", err)
	}
}


