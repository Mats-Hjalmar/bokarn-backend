# Backend

Guidance for AI agents and contributors working in this repository.

## Project

Go service (`github.com/Mats-Hjalmar/bokarn-backend`), Go 1.26, Postgres 18,
Redis, Ory Kratos, OpenTelemetry.

## After every code change

Always run these, in order, and make sure they pass before considering a change
done:

1. `make fmt` — goimports + golines.
2. `make lint` — must report no errors.

## Commands

| Command | What it does |
|---|---|
| `make dev` | infra + migrations + `air` hot reload on :1437 |
| `make up` / `make down` / `make nuke` | infra only; `nuke` also erases volumes |
| `make migrate` | apply migrations (connects as the migrator role) |
| `make migrate-new name=<slug>` | create a new tern migration |
| `make jobs` | list registered background jobs |
| `make job NAME=<job>` | run one background job to completion |
| `make test` / `make test-e2e` | unit tests / the separate e2e module |

## The three database roles

They are not interchangeable and the split is the whole security posture:

| Role | Powers | Used by |
|---|---|---|
| `bokarn_migrator` | owns the schema, `BYPASSRLS` | `cmd/migrate` only |
| `bokarn_app` | DML only, **`NOBYPASSRLS`**, owns nothing | `cmd/api`, `cmd/job` |
| `bokarn_platform` | `BYPASSRLS` | `internal/platform` only, every call audited |

`FORCE ROW LEVEL SECURITY` does not stop a superuser, so `db.AssertRestrictedRole`
runs at boot and the process exits rather than serving one tenant's data to
another. Never "fix" a permission error by pointing the API at the migrator DSN.

## Package layout

- No `controllers/` / `services/` / `repositories/` trees. Subsystems are
  top-level directories under `internal/`.
- Domain packages must not import `net/http`, minmux, or any HTTP DTO. All
  `net/http` and router wiring lives in `internal/httpx`, one
  `<subsystem>_endpoint.go` per subsystem and `<subsystem>_admin_endpoint.go`
  for the staff split.
- Files: `<name>.go` (package doc stating what the domain owns *and where it
  deliberately stops*, sentinel errors, types, pure helpers) plus `store.go`. A
  `service.go` must earn its place by orchestrating across stores; a single
  store operation is called from the handler directly.
- Split with a typename **prefix**, never a suffix: `store.go` +
  `store_admin.go`, `service.go` + `service_quote.go`.
- Prefer flat packages. A sub-folder only when a sub-feature needs ~10+ files.
- Background work is registered in `internal/worker`, which both `cmd/api` and
  `cmd/job` build from. A job that ticks in one binary but is unreachable in the
  other is a job nobody can test.

## Occupancy and the freeze

Two rules that most of `internal/booking` exists to keep:

- **Never count inventory.** The `unit_allocation_no_overlap` exclusion
  constraint is the only concurrency authority. Nothing asks whether a pitch is
  free before writing: the assignment loop walks scored candidates inside
  savepoints and lets `23P01` decide. Asking first is both a race and a second
  implementation of the same rule.
- **Never reprice a confirmed booking.** The breakdown is copied verbatim onto
  `booking_price_line`, which is append-only. Confirming compares a recomputed
  `pricing.InputHash` against the stored one and refuses a mismatch rather than
  quoting again — a guest who saw one total is told it moved, not charged the new
  one.

Hold expiry is an explicit state write in two halves: opportunistically at the
top of any path that needs to know what is free, and on a sweeper. The index
predicate must be `IMMUTABLE`, so `now()` cannot appear in it, which means an
expired hold occupies its unit until something writes the new state.

## Idioms

- **Inline the SQL.** Pass the backtick string directly to
  `tx.Query`/`QueryRow`/`Exec`. Never hoist into a `const`, never build a shared
  `cols` const; spell the columns out in every query even when repeated.
- Transactions: `tx, err := …; defer func() { _ = tx.Rollback(ctx) }(); …;
  tx.Commit(ctx)`. Store methods that must join a caller's transaction take
  `q db.TX` as the **second** parameter.
- Error wrapping: lowercase verb-phrase plus `: %w` — `"begin tx: %w"`,
  `"insert booking: %w"`. Always `return rows.Err()` after a row loop.
- Map Postgres errors to sentinels at the bottom of `store.go` with
  `errors.As(&pgErr)` against the `db` SQLSTATE constants.
- `var logger = logging.New("<name>")` per package — **not** `slog.With`, which
  captures whatever handler is default at variable-initialisation time and so
  depends on whether the package happens to import `internal/otel`. See
  `findings/logging-and-package-init.md`. Use `InfoContext`/`ErrorContext` when a
  ctx is in scope; errors as `"err", err`.
- Line width 80 (`golines -m 80`).

## Routes

Every route carries `openapi.Summary`, `openapi.Tags`, a security annotation
(`openapi.Security(scheme, perm…)` or `openapi.NoSecurity()`), and a
`ReturnsBody` for **every** status the handler can return, with
`router.ProblemDetails` for RFC-7807 errors. `internal/httpx/problem.go` is the
entire error writer.

## Migrations

tern, `migrations/NNN_name.sql`, `---- create above / drop below ----`. Never
migrate on application startup. Migration comments are the one place dense prose
is expected: explain *why* a table or constraint exists, because the constraint
is usually the load-bearing part.

## Background jobs

A job is registered once in `internal/jobs` and reached two ways: a ticker in
`cmd/api` and `cmd/job run <name>`. Both go through `jobs.RunOnce`, which holds
a transaction-scoped advisory lock, so replicas and an operator cannot collide.
Never write a ticker that has no `cmd/job` entry — an untriggerable job cannot
be smoke-tested.
