# Release: Enterprise Security Suite

Public release of the Guardian privacy gateway, bundling all six verified upgrades
into a clean, contributor-owned repository.

## What's bundled

1. **JWT role/team-based masking + allow-list** (`golang-jwt/jwt`)
2. **Hot-reloadable `config/custom-rules.json`** (fsnotify directory watcher)
3. **True SSE streaming + on-the-fly token rehydration** (`rehydrate.go`)
4. **FAILURE_POLICY=BLOCK|PASS**
5. **Per-team cost tracking with `429` budget cap**
6. **Capped Redis audit log with key-restricted reveal** (`/api/reveal` + `AUDIT_REVEAL_KEY`)

## Verification

- **Dashboard** (`dashboard/`): `next build` exit 0; dynamic routes `/`, `/audit`, `/costs`, `/api/reveal`.
- **Gateway** (`gateway/`): `gofmt -l` clean, `go vet ./...` clean, `go build ./...` clean,
  `go test -count=1 ./...` → ok (20 tests including streaming split-token, fsnotify hot reload,
  fail-open/fail-closed, 429 message, and Redis integration via in-process miniredis).
- **README**: `## Advanced Enterprise Capabilities` below Quick Start, zero real emoji in the ASCII
  architecture diagram.
- **Attribution**: every commit authored and committed by Pourush Nair; no AI/codebuff references.

## Runtime requirements

- Redis (for token sink, budgets, and the capped audit log). Tests run against in-process miniredis.
- Optional: a Presidio `/analyze` endpoint (or `MOCK_UPSTREAM=true`) for PII scanning.
- Config via `CONFIG_PATH`/`custom-rules.json`, `JWT_SECRET`, and optional `AUDIT_REVEAL_KEY`.
