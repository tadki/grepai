package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests added by the SEE-1597 O-9 B pre-review hardener: each kills a
// specific surviving mutant from the isolated matrix at 046900c
// (see see1597-h1-hardener.json on the issue).

// TestSecretDenyDirNamesAsPaths pins that credential directory names are
// denied when the path itself is that directory at any depth (kills
// SD5-dirbasename-off: the whole-directory rule's basename leg).
func TestSecretDenyDirNamesAsPaths(t *testing.T) {
	ignore, err := NewIgnoreMatcher(t.TempDir(), nil, "", nil)
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	for _, p := range []string{".aws", ".ssh", ".gnupg", ".gpg", "deep/nested/.aws"} {
		if !ignore.IsSecretDenied(p) {
			t.Fatalf("%s must be denied by the directory-name rule", p)
		}
	}
}

// TestSecretDenyAllowlistBoundaries pins allowlist exactness: no directory
// inheritance, no globbing, case-sensitive matching (kills AL2-allow-inherit,
// AL3-allow-glob, AL4-allow-casefold).
func TestSecretDenyAllowlistBoundaries(t *testing.T) {
	ignore, err := NewIgnoreMatcher(t.TempDir(), nil, "", []string{"config", "*.pem", ".env.example", "keys/server.pem"})
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	for _, p := range []string{
		"config/.env.example", // allow "config" must not inherit into the directory
		"server.pem",          // allow "*.pem" is an exact file name, not a glob
		".ENV.example",        // allowlist matching is case-sensitive
		"KEYS/server.pem",     // directory case must match the exact entry too
	} {
		if !ignore.IsSecretDenied(p) {
			t.Fatalf("%s must stay denied: allow entries are exact, non-globbed, case-sensitive", p)
		}
	}
	if ignore.IsSecretDenied("keys/server.pem") {
		t.Fatalf("exact allow entry keys/server.pem must exempt exactly that path")
	}
}

// TestScanSecretSkipAnnotationAndScanFile pins the Scan() "(secret)" skip
// annotation and the ScanFile deny guard (kills SC2-scan-off,
// SC4-annotation-dropped, SC3-scanfile-off).
func TestScanSecretSkipAnnotationAndScanFile(t *testing.T) {
	root := t.TempDir()
	ignore, err := NewIgnoreMatcher(root, nil, "", nil)
	if err != nil {
		t.Fatalf("NewIgnoreMatcher: %v", err)
	}
	scanner := NewScanner(root, ignore)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("FAKE_SECRET=placeholder-not-a-real-value\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc ok() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	files, skipped, err := scanner.Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	annotated := false
	for _, s := range skipped {
		if s == ".env (secret)" {
			annotated = true
		}
	}
	if !annotated {
		t.Fatalf("Scan skip list must carry the explicit \"(secret)\" reason, skipped=%v", skipped)
	}
	for _, f := range files {
		if f.Path == ".env" {
			t.Fatalf(".env must never be returned by Scan, files=%v", files)
		}
	}

	fi, err := scanner.ScanFile(".env")
	if err != nil {
		t.Fatalf("ScanFile(.env): %v", err)
	}
	if fi != nil {
		t.Fatalf("ScanFile must refuse secret material, got FileInfo for %s", fi.Path)
	}
	safe, err := scanner.ScanFile("main.go")
	if err != nil || safe == nil {
		t.Fatalf("safe file must scan (err=%v, fi=%v)", err, safe)
	}
}
