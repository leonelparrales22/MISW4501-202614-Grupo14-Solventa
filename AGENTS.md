# AI Agent Guidelines — Solventa Backend

Operational, architectural and quality rules that **every AI agent** (Claude Code, OpenAI Codex, Gemini CLI, Cursor, Copilot, Windsurf, etc.) must follow when working in this repository.

---

## 1. Project Context and Architecture

Solventa is a digital insurtech platform based on Open Finance, built as **microservices bounded by business capability** in Go (1.23+) and deployed on AWS (ECS Fargate, RDS PostgreSQL Multi-AZ, ElastiCache Redis, Amazon SNS/SQS).

Design documentation (architecture, decisions, experiments, backlog) lives in the project Wiki; this repository documents what changes together with the code. **Read the README of a service before modifying it.**

### The 5 microservices (`services/`)
- `origination`: quoting, profiling (Open Finance/Open Data), actuarial rating, underwriting, issuance and billing.
- `claims`: filing, triage, assessors, on-site assistance, parametric claims and evidence.
- `partners`: B2B onboarding, credentials and performance dashboard for embedded-insurance partners.
- `audit`: immutable (*append-only*) record of underwriting decisions and consent traceability.
- `notifications`: push notifications (Firebase Cloud Messaging).

---

## 2. Non-negotiable Architecture Rules (Hard Boundaries)

1. **Strict isolation between services:**
   - A service **NEVER** imports packages from another service.
   - Inter-service communication is exclusively:
     - Synchronous: HTTP through the API Gateway.
     - Asynchronous: events through Amazon SNS/SQS (publish with *outbox*, consume idempotently).
   - The **only** thing a service may import from this repository (besides its own module) is the shared module `pkg/`.
   - Enforced with `depguard` in `.golangci.yml`.
2. **Ports and Adapters (hexagonal):**
   - The service core (`internal/domain/`, `internal/application/`, `internal/ports/`) **must not depend** on adapters or infrastructure libraries (`chi`, `pgx`, `database/sql`, `redis`, `aws-sdk`).
   - Adapters (`internal/adapters/{http,postgres,messaging,redis,s3,fcm}`) implement the ports defined in `internal/ports/`.
3. **Database per service:**
   - Each microservice exclusively owns its PostgreSQL schema. No service queries or joins against another service's database.
4. **Isolated external stubs:**
   - Modules in `stubs/` simulate external providers. No service imports code from `stubs/`; services reach them only over HTTP through the adapters in `pkg/adapters/`.

---

## 3. Language and Style Conventions

- **Everything in the repository is written in English:** code (identifiers, packages, endpoint paths), comments, log messages, OpenAPI descriptions, commit messages, and files such as this one.
- **The exceptions are `README.md` files and pull request titles and descriptions, which are written in Spanish** (team agreement).
- **Error handling and security:**
  - Do not use `log.Fatal` or `panic` in business logic or HTTP handlers; propagate errors. Both are acceptable only in `cmd/<service>/main.go` for startup failures.
  - HTTP servers must always set explicit timeouts (`ReadHeaderTimeout: 5 * time.Second` at minimum) to satisfy `gosec`.
  - Never hard-code credentials, API keys or secrets. Use environment variables.

---

## 4. API and Event Contracts (Spec-Driven)

1. **APIs (OpenAPI 3.0):**
   - Any endpoint change or addition must be reflected in `services/<service>/api/openapi.yaml`.
   - Contracts are validated in CI with `Schemathesis`.
2. **Domain events:**
   - Every new event published or consumed must be registered in `docs/events/README.md`, specifying publisher, consumers and payload.

---

## 5. Definition of Done (DoD)

Before considering any task complete, the agent **MUST run and verify locally**:

```bash
# 1. Static analysis and linter (must finish with 0 issues)
make lint

# 2. Unit tests with coverage report (coverage must be >= 70%)
make test

# 3. Build all microservices
make build
```

If any of these fails, fix the problem before finishing. Never report as verified something that was not run; say which commands could not be run and why.

**Temporary exception (project bootstrap, approved by the team, see `docs/decisions/README.md` #13):** while the services have no test suite yet, the 70 % coverage requirement (step 2) is waived. `make test` must still run without errors, and every PR must state the current coverage and the test gap in "Notes for the reviewer". The requirement applies again once the team decides that the first stories include tests.

---

## 6. Living Documentation (Docs as Code)

- If a module is added, renamed or split inside `services/<service>/internal/`, update the responsibilities table in that service's `README.md`.
- **Technical and architectural decisions are made by the team, not by the agent.** Ask the user before deciding, and do not create decision records (ADRs) on your own initiative.
- **Decision log:** `docs/decisions/README.md` records the repository decisions and their rationale. Read it for context before proposing changes to conventions, tooling or structure. Add a row only after the user approves a decision; never delete rows (mark them *Reemplazada* instead).

---

## 7. Git Flow and Commits

- **Never commit or push directly to `main` or `develop`.**
- Work in branches named:
  - `feature/<story-id>-<short-description>` (e.g. `feature/HA-L01-parallel-quoting`)
  - `fix/<story-id>-<short-description>`
  - `chore/<short-description>`
- **Conventional Commits:**
  - `feat(<service|pkg>): <description>`
  - `test(<service|pkg>): <description>`
  - `fix(<service|pkg>): <description>`
  - `docs: <description>`
  - `chore: <description>`
- When adding a Go dependency to a service, keep `go.work` and `go.mod` in sync and clean (`go mod tidy`).

### 7.1 Pull Requests and merge strategy

```
feature/* · fix/* · chore/*  ──(PR, squash)──▶  develop  ──(PR, merge commit)──▶  main
```

| From → To | Merge strategy | Why |
|---|---|---|
| `feature/*`, `fix/*`, `chore/*` → `develop` | **Squash** | One clean commit per PR/story, easy to revert; intermediate commits ("wip", "fix") never reach `develop` |
| `develop` → `main` | **Merge commit** (never squash or rebase) | Keeps `develop`'s history and prevents both branches from diverging after each release |
| Urgent hotfix | Branch from `main` → PR to `main` with **squash**, then bring the change back to `develop` | Exceptional case |

**Rules**

1. Every work PR targets **`develop`**. Only release PRs target `main`.
2. **One PR = one story or task.** Keep it small and reviewable; do not mix changes across services unless the task requires it.
3. **The PR title becomes the commit message** after squash: it must follow Conventional Commits and include the story ID when applicable, e.g. `feat(origination): parallel quoting (HA-L01)`. Release PRs use `release: <date or version>`.
4. Before opening the PR: run the Definition of Done (section 5) and update the affected documentation (section 6).
5. Keep the branch up to date with `develop` (`git fetch && git rebase origin/develop`). Never force-push shared branches.
6. To merge: green CI (`backend-ci`) and at least one approval from another team member. Nobody merges their own PR without review.
7. After merging, **delete the feature branch**. Never delete `develop` or `main`.
8. Use the PR template at `.github/pull_request_template.md`.

**For AI agents:** open a PR only when the user asks; never merge it or push directly to `main` or `develop`.

---

## 8. Guardrails and Forbidden Anti-patterns

- **No scope creep:** modify only the service or package assigned to your task. Do not touch other services unless the task is explicitly about the cross-cutting `pkg/` module.
- **No production mocks:** never add temporary mocks or simulations to production code in `services/`. Third-party simulation belongs in `stubs/`, and test mocks must live in `*_test.go` files.
- **Preserve code and context:** never delete business comments, annotations or existing code unrelated to your change.
- **Keep these rules alive:** if you identify a repeated error pattern or a common hallucination, add the corresponding restriction to this file to prevent recurrence across the team.
