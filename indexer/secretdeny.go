package indexer

import (
	"path/filepath"
	"strings"
)

// DefaultSecretAllow is the default security allowlist: empty. Any entry is
// an explicit, auditable exact relative path chosen by the project owner in
// .grepai/config.yaml (security.allow). Nothing is exempted by default —
// including files that merely look like examples (.env.example).
var DefaultSecretAllow = []string{}

// secretDenyDirNames are directory basenames whose entire subtree holds key
// or credential material.
var secretDenyDirNames = []string{".env", ".aws", ".ssh", ".gnupg", ".gpg"}

// secretDenyBaseNames are file basenames (any directory level) that are
// high-signal key or credential stores. All entries are named patterns; no
// broad extension or config* globs are used, to keep the false-positive
// surface on normal source code near zero.
var secretDenyBaseNames = []string{
	".htpasswd",
	".netrc",
	".git-credentials",
	".npmrc",
	".pypirc",
	"keystore",
}

// secretDenySuffixes are file name suffixes that denote key containers.
var secretDenySuffixes = []string{
	".pem",
	".key",
	".p12",
	".pfx",
	".jks",
	".keystore",
	".kdbx",
	".ppk",
	".private",
}

// secretDenyPrefixNames are basename prefixes for well-known private key
// pairs (id_rsa, id_rsa_old, id_ed25519_enc, ...).
var secretDenyPrefixNames = []string{
	"id_rsa",
	"id_dsa",
	"id_ecdsa",
	"id_ed25519",
}

// secretDenyGlobs are named credential-file globs (no broad *.json/*.yaml).
var secretDenyGlobs = []string{
	"service-account*.json",
	"service_account*.json",
}

// isSecretDeniedPath reports whether a project-relative slash path carries
// secret or key material under the default deny rules. Allowlist exemption
// is handled by the caller (IsSecretDenied), so this function is purely the
// deny-rule evaluation and never consults user ignore configuration.
func isSecretDeniedPath(relPath string) bool {
	if relPath == "" || relPath == "." {
		return false
	}
	clean := filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	base := strings.ToLower(filepath.Base(clean))

	for _, dir := range secretDenyDirNames {
		if base == dir {
			return true
		}
	}
	for _, name := range secretDenyBaseNames {
		if base == name {
			return true
		}
	}
	for _, suffix := range secretDenySuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, prefix := range secretDenyPrefixNames {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	for _, glob := range secretDenyGlobs {
		if ok, _ := filepath.Match(glob, base); ok {
			return true
		}
	}

	// Whole-directory deny: any ancestor segment named like a credential
	// directory (e.g. .env/kol-mcp.env, .aws/credentials) denies the file.
	dir := filepath.ToSlash(filepath.Dir(clean))
	for _, seg := range strings.Split(dir, "/") {
		for _, d := range secretDenyDirNames {
			if seg == d {
				return true
			}
		}
	}

	// Dot-env family: basenames starting with ".env" (.env, .env.local,
	// .env.production) and files ending in ".env" (app.env, config.env).
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") {
		return true
	}
	return false
}

// securityAllowSet converts an allowlist slice into a lookup set of exact
// slash-normalized relative paths. Only exact matches exempt a file; no
// globbing, no directory-prefix inheritance — allowlist entries are minimal
// and auditable by design.
func securityAllowSet(allow []string) map[string]bool {
	set := make(map[string]bool, len(allow))
	for _, a := range allow {
		trimmed := strings.Trim(filepath.ToSlash(strings.TrimSpace(a)), "/")
		if trimmed != "" {
			set[trimmed] = true
		}
	}
	return set
}
