---
title: Secret Deny (Default Security Rules)
description: Default-on protection that keeps secrets, keys and credential files out of the grepai index
---

grepai ships a **default-on secret deny layer** that keeps sensitive files out of the index in both single-project mode and workspace mode, during the initial scan and in the watch incremental chain.

## How it works

The deny layer is evaluated **before and independently of** `.grepaiignore`, `.gitignore` and every user ignore rule:

1. **security.allow** (optional, default empty): an exact project-relative path that is exempt from the deny rules.
2. **Security deny rules** (below): if the path matches, it is never indexed — no negation, no ignore rule, no re-include pattern can lift the deny.
3. **Normal ignore chain**: `.grepaiignore` → `.gitignore` / config `ignore` list, unchanged.

If a denied file was already indexed (e.g. by an older grepai version), the next initial scan — and any watch event touching that path — **removes it from the index** instead of indexing it.

## Default deny patterns

Any path component or file name matching below rules is denied (case-insensitive on the file name):

### Env family

- Basename starting with `.env`: `.env`, `.env.local`, `.env.production`, `.env.example`, …
- Basename ending with `.env`: `app.env`, `config.env`, `prod.env`, …
- Any file inside a directory named `.env` (the whole `.env/` directory is denied)

### Key and key-container files

- Suffixes (any directory): `.pem`, `.key`, `.p12`, `.pfx`, `.jks`, `.keystore`, `.kdbx`, `.ppk`, `.private`
- Basename prefixes: `id_rsa`, `id_dsa`, `id_ecdsa`, `id_ed25519` (e.g. `id_rsa`, `id_ed25519_enc`)
- Basename exactly `keystore`

### Credential stores (basename, any directory)

- `.htpasswd`, `.netrc`, `.git-credentials`, `.npmrc`, `.pypirc`
- Directories: `.aws/`, `.ssh/`, `.gnupg/`, `.gpg/` (entire subtree)
- Named credential globs: `service-account*.json`, `service_account*.json`

### Explicitly NOT denied

No broad `*.json`, `*.yaml`, `config*` or similar rules exist. Examples of safe files that remain indexed: `main.go`, `config/settings.yaml`, `src/sample.env.txt` (does not start or end with `.env`), `environment.ts`, `docs/env-guide.md`.

## Allowlist (opt-in exemptions)

`allow` defaults to **empty**. To exempt an exact file, add its exact project-relative path (slash-separated, no globs) to `.grepai/config.yaml`:

```yaml
security:
  allow:
    - .env.example
```

Boundary rules:

- Entries are **exact paths only** — no globs, no `*` patterns.
- An entry applies to exactly that path; allowing a file in a subdirectory requires its own full relative path. Allowing a directory exempts nothing inside it.
- An allow entry only lifts the **security deny**. Normal ignore rules (`.gitignore`, `ignore` list) still apply afterwards and keep the final say on whether the file is walked.
- Every allow entry is plain text in `.grepai/config.yaml` and therefore fully auditable.

Example boundaries:

| Entry in `security.allow` | `.env.example` (root) | `sub/.env.example` | `.env` |
| --- | --- | --- | --- |
| `.env.example` | exempt | **still denied** | still denied |

## Behavior in the index

- **Initial scan**: denied files are skipped (reported as `(secret)` in the skip list); documents already in the index that are denied on the next scan are removed.
- **Watch increments**: any create/modify/delete event on a denied path evicts the path from the index and never indexes its content.

## Rollback

To disable the layer entirely (not recommended), remove the deny evaluation in `indexer/gitignore.go` (`ShouldIgnore` / `ShouldSkipDir` / `Scanner`) — or, for per-file exemptions, use `security.allow` as above. Denying is strictly safer than allowing: when in doubt, do not add an allow entry.
