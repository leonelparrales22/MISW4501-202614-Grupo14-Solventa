# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

The repository rules (architecture, language, Definition of Done, Git and Pull Request flow) live in `AGENTS.md`. They are not duplicated here so the two files cannot drift apart.

## Claude Code specifics

- Meet the Definition of Done in `AGENTS.md` (section 5) before finishing a task.
- **Technical or architectural decisions:** ask the user before deciding; do not generate decision files (ADRs) on your own initiative.
- Do not commit, push or open a PR unless the user asks. Follow sections 7 and 7.1 of `AGENTS.md`.
- Read a service's `README.md` before modifying that service.

## Commands

The repo is a Go workspace (`go.work`): one module per service, plus `pkg/` and each stub. Run every command from the repo root.

```bash
make lint    # golangci-lint run ./pkg/... ./services/<each>/...
make test    # go test -cover ./pkg/... ./services/<each>/...
make build   # go build -o bin/<svc> ./services/<svc>/cmd/<svc>, for each service

# One package / one test
go test ./services/origination/internal/profiling/... -run TestName -v
go test ./pkg/resilience -run TestOpensAfterConsecutiveFailures -v

# Run a service locally (see its README for the env vars)
REDIS_ADDR=localhost:6379 OPEN_FINANCE_URL=http://localhost:9000 go run ./services/origination/cmd/origination
```

- `golangci-lint` must be **v2.x** (`.golangci.yml` uses `version: "2"`; CI pins v2.1.6). gofmt and goimports run as formatters.
- If `make` is not installed (e.g. on Windows), run the `Makefile` commands directly with the same package lists:
  ```bash
  S="origination claims partners audit notifications"
  golangci-lint run ./pkg/... $(for s in $S; do printf './services/%s/... ' $s; done)
  go test -cover ./pkg/... $(for s in $S; do printf './services/%s/... ' $s; done)
  for s in $S; do go build -o bin/$s ./services/$s/cmd/$s; done
  ```
- The `Makefile` and CI skip `stubs/`. Lint and test a stub on its own: `golangci-lint run ./stubs/<stub>/...` and `go test -cover ./stubs/<stub>/...`.
- `services/<svc>/tests/integration` has no build tags, so `make test` runs it. `pkg/security/cognito_staging_integration_test.go` skips unless `AUTH_STAGING_ISSUER` (and related vars) are set.
- Cross-service load, resilience and security scenarios (k6, Toxiproxy, Schemathesis, ZAP) live in `tests/<kind>/<story-id>/`, each with its own README. Local Toxiproxy and LocalStack configs live in `infra/local/`.

## Architecture notes (beyond AGENTS.md)

- **How boundaries are enforced:** `depguard` in `.golangci.yml` has one rule per service that denies the *other* services and `stubs/` (it matches by prefix, so one rule on `services/` would also block a service's own imports, see decision #7). A `pure-core` rule bars `internal/{domain,application,ports}` from importing adapters, chi, pgx, `database/sql`, go-redis and the AWS SDK. A new service or infrastructure library needs a matching update to these rules.
- **Business modules sit beside the hexagon:** besides `domain/application/ports/adapters`, each service has capability modules under `internal/` (e.g. origination's `orchestrator`, `profiling`, `rating`, `billing`). They call each other in-process. The responsibilities table in the service README must list every module.
- **External providers:** the HTTP client for each provider is in `pkg/adapters/<provider>` (700 ms hard timeout plus a `pkg/resilience` circuit breaker). The service's `internal/adapters/<provider>` bridges that client to a service port. Example: origination's `profiling.ParallelSource` fans out to Open Finance, Open Data and KYC with a 120 ms budget per source, then degrades to the Redis cache and then a base actuarial profile. In development and experiments, the providers are the `stubs/` modules (ports 9000/9010/9020…), often behind Toxiproxy.
- **Composition root:** `cmd/<svc>/main.go` reads all config from env vars (`loadConfig(getenv)`, which tests can call), wires adapters into the core and sets the HTTP server timeouts. `log.Fatal` is allowed only here.
- **Implementation status:** most folders are reserved placeholders. Each README has an **Estado** line that says what really exists, tagged by story ID (HA-D01, HA-L01, HA-S01…). When you implement a story, update that line and reference the story ID in code comments and docs the way existing code does.
- **Docs to keep in sync:** `services/<svc>/api/openapi.yaml` (endpoints), `docs/events/README.md` (events), `docs/api/README.md` (auth contract, `AUTH_*` config), the env var table in the service README, and `docs/decisions/README.md` (only after the user approves a decision).
