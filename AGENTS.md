# Repository Guidelines

## Structure

- `cmd/mt-server/` is the thin executable entry point.
- `internal/app/` is the only composition root.
- `internal/platform/` owns shared transport, infrastructure configuration, persistent state, authentication, health, and location behavior.
- `internal/modules/` contains feature modules. A module owns its domain models, service behavior, cache, and HTTP handlers.
- `internal/providers/` contains upstream adapters and must not expose raw provider models to device handlers.
- `api/openapi.json` is the public device contract. `deploy/` contains public templates only.

## Commands

```sh
make format
make check
make build
make docker-build
```

Run `go test -race ./...` for every concurrency or cache change. Real QWeather calls are manual deployment validation and must never run in CI.

## Architecture

Keep the service a modular monolith with explicit compile-time module registration. Do not add dynamic plugins, arbitrary reverse proxying, databases, Redis, or generic repositories without a concrete feature that needs them. Shared platform code must not import feature packages.

The v1 API is no longer strictly additive: the fixed `X-MT-Location-*` headers became all-or-nothing with an IP-inference fallback, and `GET /api/v1/location` was added. Renaming fields, changing types or units, or removing fields still requires a new API version and coordinated firmware work. Provider response structs stay private to their adapter.

## Security And Privacy

Never commit or log real hostnames, public or private IP addresses used by the deployment, coordinates, QWeather identifiers, tunnel identifiers, credentials, tokens, JWTs, private keys, or complete Authorization headers. Public examples use reserved documentation domains and addresses only.

Application secrets live only in the private state volume. Production source ports remain unexposed. Authenticated devices supply locations only through the fixed `X-MT-Location-*` contract (all required headers together or none) or through the configured trusted-proxy IP inference; preserve authentication-first parsing, strict validation, privacy-grid normalization, per-device grid-change limiting, the ban on coordinate/IP logging or response fields, and the rule that forwarded client-IP headers are honored only from peers in `MT_TRUSTED_CLIENT_IP_NETS`. Do not add query-parameter tokens or coordinates.

## Style And Tests

Use `gofmt`, standard-library conventions, short package names, wrapped errors, bounded I/O, explicit timeouts, and context-aware blocking. Keep comments for exported APIs and non-obvious invariants. Tests use `httptest`, fake clocks/providers, and sanitized fixtures.

## Commits

Use Header-Body-Footer commits: `<type>(<scope>): <subject>`, then motivation and impact, then `Refs:` or `BREAKING CHANGE:`. Do not commit, push, tag, release, deploy, or rotate credentials unless explicitly requested.
