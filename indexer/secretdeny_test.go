package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yoanbernabeu/grepai/store"
)

// TestSecretDenyRuleMatrix pins the default deny rule surface documented in
// docs/src/content/docs/security.md.
func TestSecretDenyRuleMatrix(t *testing.T) {
	denied := []string{
		".env",
		".env.local",
		".env.production",
		".env.example", // example-looking files are denied by default too
		"config/app.env",
		"deploy/prod.env",
		".env/kol-mcp.env", // whole .env directory denied
		"keys/server.pem",
		"certs/ca.key",
		"id_rsa",
		"id_rsa_old",
		"id_ed25519_enc",
		"backup.p12",
		"cert.pfx",
		"app.jks",
		"keystore",
		"db.kdbx",
		"session.ppk",
		"token.private",
		".htpasswd",
		".netrc",
		".git-credentials",
		".npmrc",
		".pypirc",
		".aws/credentials",
		".ssh/id_ed25519",
		"deploy/service-account-prod.json",
		"scripts/service_account.json",
		// Uppercase directory variants must match identically (MEDIUM-1 R1-O9B):
		".ENV/plain.txt",
		".AWS/credentials",
		".GPG/data.txt",
		".ENV/kol-mcp.env",
		".SSH/id_rsa",
	}
	for _, p := range denied {
		if !isSecretDeniedPath(p) {
			t.Errorf("%q must be denied by default", p)
		}
	}

	allowed := []string{
		"main.go",
		"README.md",
		"config/settings.yaml", // no broad config* deny
		"src/sample.env.txt",   // neither starts with .env nor ends with .env
		"handler.go",
		"environment.ts", // "environment" is not the .env family
		"docs/env-guide.md",
		"keynote.md", // "keynote" does not end in .key
		// Uppercase names that do NOT match any deny rule stay safe —
		// case-insensitivity must not over-reach.
		"SRC/ReadMe.md",
		"Vendor/main.go",
	}
	for _, p := range allowed {
		if isSecretDeniedPath(p) {
			t.Errorf("%q must NOT be denied (false positive)", p)
		}
	}
}

// TestIsSecretDeniedAllowlistExactPaths pins the allowlist boundary: entries
// are exact project-relative paths, no globbing, no directory inheritance.
func TestIsSecretDeniedAllowlistExactPaths(t *testing.T) {
	m, err := NewIgnoreMatcher(t.TempDir(), nil, "", []string{".env.example"})
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	if m.IsSecretDenied(".env.example") {
		t.Fatal("exact allowlisted path must be exempt")
	}
	if !m.IsSecretDenied(".env") {
		t.Fatal("non-allowlisted .env must stay denied")
	}
	// Directory-prefix inheritance is not allowed: allowing ".env.example"
	// at root does not exempt "sub/.env.example".
	if !m.IsSecretDenied(filepath.Join("sub", ".env.example")) {
		t.Fatal("allowlist must be exact-path only, no directory inheritance")
	}
	// Allowing a directory does not exempt files inside it.
	if !m.IsSecretDenied(filepath.Join("envdir", ".env")) {
		t.Fatal("directory allowlist entries must not exempt children")
	}
}

// TestSecretDenyNotBypassedByNegation pins the mandatory-floor semantics: a
// .grepaiignore negation (or any user ignore rule) cannot lift the deny.
func TestSecretDenyNotBypassedByNegation(t *testing.T) {
	root := t.TempDir()
	gi := filepath.Join(root, ".grepaiignore")
	if err := os.WriteFile(gi, []byte("!.env\n!.env.local\n!*.pem\n"), 0o644); err != nil {
		t.Fatalf("write .grepaiignore: %v", err)
	}
	m, err := NewIgnoreMatcher(root, nil, "", nil)
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	for _, p := range []string{".env", ".env.local", "keys/server.pem"} {
		if !m.IsSecretDenied(p) {
			t.Errorf("%q must stay denied despite negation patterns", p)
		}
		// And ShouldIgnore must also report ignored: deny is a floor.
		if !m.ShouldIgnore(p) {
			t.Errorf("%q must be ignored overall (deny floor)", p)
		}
	}
}

// TestSecretDenyDirSkip pins that a denied directory is skipped outright in
// directory pruning, and that a negation cannot rescue it.
func TestSecretDenyDirSkip(t *testing.T) {
	root := t.TempDir()
	gi := filepath.Join(root, ".grepaiignore")
	if err := os.WriteFile(gi, []byte("!.env/\n"), 0o644); err != nil {
		t.Fatalf("write .grepaiignore: %v", err)
	}
	m, err := NewIgnoreMatcher(root, nil, "", nil)
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	if !m.ShouldSkipDir(".env") {
		t.Fatal("denied .env directory must be pruned")
	}
	if !m.ShouldSkipDir(filepath.Join(".env", "sub")) {
		t.Fatal("denied .env subdirectory must be pruned")
	}
	if m.ShouldSkipDir("src") {
		t.Fatal("normal directories must not be pruned")
	}
}

// TestSecretDenyScanEviction is the end-to-end eviction proof for §SPEC-004A:
// a secret file already present in the index is removed by the next initial
// scan once the deny layer is active, while safe files stay indexed.
func TestSecretDenyScanEviction(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Real secrets are forbidden in fixtures: these are obvious placeholders.
	write(".env", "FAKE_SECRET=placeholder-not-a-real-value\n")
	write("main.go", "package main\n\nfunc main() {}\n")

	ignore, err := NewIgnoreMatcher(root, nil, "", nil)
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	chunker := NewChunker(512, 50)
	idx := NewIndexer(root, store.NewGOBStore(filepath.Join(root, "index.gob")), newMockEmbedder(), chunker, NewScanner(root, ignore), time.Time{})

	ctx := context.Background()
	// Seed the index as the pre-deny era would have: construct the FileInfo
	// directly (ScanFile is deny-guarded now, which is exactly the fix).
	content := "FAKE_SECRET=placeholder-not-a-real-value\n"
	hash, err := HashFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatalf("hash fixture: %v", err)
	}
	fi := &FileInfo{Path: ".env", Size: int64(len(content)), Hash: hash, Content: content}
	if _, err := idx.IndexFile(ctx, *fi); err != nil {
		t.Fatalf("seed index: %v", err)
	}

	// Initial scan WITH the deny layer active: .env must be evicted.
	stats, err := idx.IndexAll(ctx)
	if err != nil {
		t.Fatalf("IndexAll: %v", err)
	}
	if stats.FilesRemoved == 0 {
		t.Fatalf("denied .env must be evicted from the index, FilesRemoved=%d", stats.FilesRemoved)
	}
	docs, err := idx.store.ListDocuments(ctx)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	for _, d := range docs {
		if d == ".env" {
			t.Fatalf(".env still indexed after deny-enabled scan: %v", docs)
		}
	}
	// main.go must be present exactly because it is safe.
	foundSafe := false
	for _, d := range docs {
		if d == "main.go" {
			foundSafe = true
		}
	}
	if !foundSafe {
		t.Fatalf("safe main.go must stay indexed, docs=%v", docs)
	}
}

// TestScannerDenySurface pins the Scanner-level deny API used by the watch
// chain and the empty/root path guards.
func TestScannerDenySurface(t *testing.T) {
	if isSecretDeniedPath("") || isSecretDeniedPath(".") {
		t.Fatal("empty and root paths must never be denied")
	}
	root := t.TempDir()
	ignore, err := NewIgnoreMatcher(root, nil, "", nil)
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	scanner := NewScanner(root, ignore)
	if !scanner.IsSecretDenied(".env") {
		t.Fatal("scanner deny passthrough must report .env denied")
	}
	if scanner.IsSecretDenied("main.go") {
		t.Fatal("scanner deny passthrough must not deny safe files")
	}
	if scanner.IsSecretDenied("") || scanner.IsSecretDenied(".") {
		t.Fatal("scanner deny must not deny empty/root paths")
	}
}

// TestScanAndMetadataSkipSecrets pins that both walking scan entrypoints
// report secret files as skipped and never return them.
func TestScanAndMetadataSkipSecrets(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("FAKE=placeholder\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	ignore, err := NewIgnoreMatcher(root, nil, "", nil)
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	scanner := NewScanner(root, ignore)

	metas, skipped, err := scanner.ScanMetadata()
	if err != nil {
		t.Fatalf("ScanMetadata: %v", err)
	}
	for _, m := range metas {
		if m.Path == ".env" {
			t.Fatal("ScanMetadata must not return the denied .env")
		}
	}
	foundSecretSkip := false
	for _, s := range skipped {
		if s == ".env (secret)" {
			foundSecretSkip = true
		}
	}
	if !foundSecretSkip {
		t.Fatalf("ScanMetadata must report .env as a secret skip, got %v", skipped)
	}

	files, _, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, f := range files {
		if f.Path == ".env" {
			t.Fatal("Scan must not return the denied .env")
		}
	}
	if len(files) == 0 {
		t.Fatal("Scan must still return the safe main.go")
	}
}
