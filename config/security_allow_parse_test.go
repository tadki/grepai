package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSecurityAllowYAMLParses pins the security.allow yaml contract of the
// O-9 B secret-deny layer: the security.allow key parses into
// Config.Security.Allow and defaults to empty (kills CF1-security-tag and
// CF2-allow-tag from the O-9 B hardener matrix, see see1597-h1-hardener.json).
func TestSecurityAllowYAMLParses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	y := "security:\n  allow:\n    - docs/keys/server.pem\n    - .env.example\n"
	if err := os.WriteFile(filepath.Join(dir, ".grepai", "config.yaml"), []byte(y), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(c.Security.Allow) != 2 || c.Security.Allow[0] != "docs/keys/server.pem" || c.Security.Allow[1] != ".env.example" {
		t.Fatalf("security.allow must parse from yaml, got %v", c.Security.Allow)
	}

	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir2, ".grepai"), 0o755); err != nil {
		t.Fatalf("mkdir2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, ".grepai", "config.yaml"), []byte("embedder:\n  provider: ollama\n"), 0o644); err != nil {
		t.Fatalf("write config2: %v", err)
	}
	c2, err := Load(dir2)
	if err != nil {
		t.Fatalf("load2: %v", err)
	}
	if len(c2.Security.Allow) != 0 {
		t.Fatalf("default security.allow must be empty, got %v", c2.Security.Allow)
	}
}
