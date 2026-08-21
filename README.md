# bokarn — backend

The Go service behind [bokarn](https://github.com/Mats-Hjalmar/bokarn): a
multi-tenant booking and operations platform for campsites and resorts.

Run it from the umbrella repository with `make dev` rather than on its own.

## What is here

| Path | |
| --- | --- |
| `cmd/api` | HTTP server |
| `cmd/migrate` | one-shot migration runner; the server never migrates on startup |
| `cmd/job` | runs one background job by name, so anything on a ticker is also runnable by hand |
| `internal/tenant` | the only route from a domain package to Postgres |
| `internal/pricing` | a pure pricing engine plus the stores that feed it |
| `internal/inventory`, `availability`, `assignment` | what an operator sells, what is free, and which unit a booking lands on |
| `internal/booking` | holds, the price freeze, and the transitions reception drives |
| `internal/outbox`, `notify` | side effects made as durable as the change that owed them |
| `internal/worker` | the job registry, built once for both `cmd/api` and `cmd/job` |
| `e2e/` | a separate module that proves tenant isolation against a real database |

## The three ideas worth knowing

**Isolation is the database's job.** Every tenant table carries `tenant_id`
under `FORCE ROW LEVEL SECURITY` with four explicit policies. The application
role is deliberately not the table owner and cannot bypass RLS — and
`tenant.New` refuses to start the process if it ever could, because a service
connected as a privileged role has every policy silently inert.

**Occupancy is a range.** Bookings, holds and maintenance blocks share one table
and one `EXCLUDE USING gist` constraint, so two stays cannot overlap on a unit
and same-day turnover needs no special case.

**Pricing is a pure function.** `internal/pricing/engine.go` imports nothing but
the standard library and `internal/money`: no database handle, no clock, no
randomness. The golden fixtures in `internal/pricing/testdata` are its
specification.

**A confirmed price is copied, never recomputed.** Confirming a booking reloads
the quote, refuses it if the request describes a different stay than was priced,
and copies the breakdown verbatim onto append-only rows. The engine is never
called for that booking again — a guest who saw one total is told if it moved
rather than charged the new one.

**Nothing here is a counter.** The exclusion constraint is the only concurrency
authority: the assignment loop walks scored candidates inserting until one
insert survives, and two guests racing for the last cabin produce exactly one
hold and one refusal, decided by Postgres.

## Commands

| | |
| --- | --- |
| `make dev` | infrastructure, migrations, seed, then the API with hot reload |
| `make test` / `make test-e2e` | unit tests / the isolation suite |
| `make rls-mutation` | removes each protection in turn and requires the suite to notice |
| `make jobs` / `make job NAME=…` | list and run background jobs |
| `./scripts/seed-bookings.sh` | demo bookings, created through the API so they carry real quotes, frozen lines and confirmation emails |

After every change: `make fmt`, then `make lint`, both clean. See `AGENTS.md`
for the conventions this codebase follows and `findings/` for durable notes.
