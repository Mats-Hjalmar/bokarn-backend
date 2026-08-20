# Multi-tenancy via row-level security

- 2026-08-19: The three Postgres roles are created in
  `docker/initdb/00-roles.sql`, outside tern, because a migration cannot safely
  create the role it is running as. `bokarn_app` is `NOSUPERUSER NOBYPASSRLS`
  and owns nothing; the database and `public` schema are owned by
  `bokarn_migrator`. `FORCE ROW LEVEL SECURITY` does not constrain a superuser
  or a `BYPASSRLS` role, so a service connected as one has every policy inert
  with no error and no failing test — which is why `tenant.New` asserts the role
  at boot and the process refuses to start otherwise.

- 2026-08-19: `current_tenant_id()` must be `language sql stable parallel safe`
  with a `nullif` guard. Each property has its own failure mode. Without
  `nullif`, once any transaction has set a custom GUC, `current_setting` with
  `missing_ok` returns `''` rather than NULL for the life of that backend and
  `''::uuid` raises `22P02` — so the system passes every test on a fresh
  connection and throws cast errors only under pool reuse. A `plpgsql` wrapper
  is not inlined by the planner, so the markings stop applying. And the
  `CREATE FUNCTION` default of `VOLATILE PARALLEL UNSAFE` drops the predicate
  out of index conditions and disables parallel plans on every tenant table.

- 2026-08-19: An unpinned INSERT surfaces as `42501`, not `23502`. The policy's
  `WITH CHECK` is evaluated before the `NOT NULL` constraint on `tenant_id`, so
  the policy refuses it first. The `NOT NULL` is still worth having: it is the
  second line of defence if RLS is ever disabled, and it is what the mutation
  script observes in that state.

- 2026-08-19: Guest requests need the operator resolved from the hostname before
  any tenant is pinned, and the policy on `tenants` returns nothing in that
  state. Solved with a `SECURITY DEFINER` function `tenant_id_for_slug(text)`
  exposing exactly the slug-to-id mapping, which is public by construction
  because the slug *is* the hostname. Rejected alternatives: a second lookup
  table duplicating the source of truth; a bespoke permissive policy on
  `tenants`, which would break the catalog guard's "every predicate is one of
  two known families" assertion; and resolving through the `BYPASSRLS` pool,
  which would put a privileged connection on every guest request. The function
  pins `search_path`, because a `SECURITY DEFINER` function that resolves names
  through the caller's `search_path` can be hijacked by a same-named relation.

- 2026-08-19: The output cache is not covered by RLS and this was verified, not
  assumed. minmux builds its key from method and path, so `GET /api/v1/sites` is
  byte-identical across operators. `tenant.Cached` is the only permitted wrapper
  and prepends the pinned operator via `VaryByCustom`. Proven three ways: two
  distinct Redis keys for one path; a row updated in the database still served
  stale within the TTL (so the cache genuinely serves rather than merely
  writes); and one operator's cached entry never returned to another. This only
  holds while `tenant.Middleware` is registered *before* the cache middleware —
  the cache key is derived from the request context.

- 2026-08-19: Staff tenancy is carried in the Kratos identity's
  `metadata_public.tenant_id`. `metadata_admin` cannot be used: it is never
  returned by the public `toSession()` endpoint the API calls. Both fields are
  writable only through `/admin/identities`, so `metadata_public` is equally
  trustworthy and merely visible to its owner. Admin routes resolve the operator
  from the identity alone and reject a mismatched `Host` with 400; letting Host
  win would make a token issued at one campsite act on another by changing a
  hostname.

- 2026-08-19: `scripts/rls-mutation.sh` is what makes the isolation suite
  meaningful. It removes each protection in turn — `disable row level security`,
  `no force`, and dropping a policy — and asserts the suite fails. First version
  ran `go test` and `docker compose exec psql` per mutation and took over ten
  minutes; compiling the test binary once and reaching Postgres directly brings
  it to about seventy seconds for 24 mutations. A check nobody runs protects
  nothing. Current state: 24 mutations, 0 survivors.
