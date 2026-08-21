#!/usr/bin/env bash
# Mutation-tests the isolation suite.
#
# A passing RLS test proves nothing on its own: it passes identically when the
# policies are enforcing and when they are absent but no test actually crosses a
# tenant boundary. So this disables the protection one table at a time, re-runs
# the suite, and asserts it FAILS. A mutation the suite does not notice is a
# hole in the tests, reported as a survivor.
#
# Nothing here may hang. Every database statement carries a lock timeout and
# every suite run carries a test timeout, because this script is the last thing
# in CI and a stalled job looks exactly like a slow one.
#
# Three things keep it fast enough to actually run. The suite is compiled
# once; Postgres is reached directly rather than through `docker compose exec`;
# and each mutation stops at the first test that notices it, because the
# question is "does the suite fail?" and the first failure is the whole answer.
# Doing any of them the obvious way turns a two-minute check into a
# twenty-minute one, and a check nobody waits for is a check that gets skipped.
#
# Every mutation is restored, including on interrupt.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

PGHOST=${BOKARN_DB_HOST:-127.0.0.1}
PGPORT=${BOKARN_DB_PORT:-1438}
SUPERUSER=${BOKARN_DB_SUPERUSER:-postgres}
export PGPASSWORD=${BOKARN_DB_SUPERUSER_PASSWORD:-postgres}

# Every mutation is DDL, and ALTER TABLE takes ACCESS EXCLUSIVE — which waits
# forever behind any open transaction. A leftover connection is enough, and on a
# slow runner that is a race this loses: two CI runs sat in this script for three
# and six hours before anyone noticed, for a check that takes ninety seconds.
# A blocked mutation now fails in five seconds and says so.
psql_su() {
	psql -h "$PGHOST" -p "$PGPORT" -U "$SUPERUSER" -d bokarn \
		-X -q -v ON_ERROR_STOP=1 \
		-c "set lock_timeout = '5s'" -c "set statement_timeout = '30s'" \
		-c "$1"
}

# A full template rather than -t: BSD mktemp treats the argument as a prefix,
# GNU requires the XXXXXX placeholder, and `-t bokarn-e2e` silently produces
# nothing usable on Linux — which surfaced as the suite "failing" its baseline.
BIN=$(mktemp "${TMPDIR:-/tmp}/bokarn-e2e.XXXXXX")
trap 'rm -f "$BIN"' EXIT

echo "Compiling the suite once..."
(cd e2e && GOWORK=off go test -c -o "$BIN" .) || exit 1

TABLES=$(psql -h "$PGHOST" -p "$PGPORT" -U "$SUPERUSER" -d bokarn -X -A -t -c "
	select c.relname
	  from pg_class c
	  join pg_namespace n on n.oid = c.relnamespace
	 where n.nspname='public' and c.relkind='r' and c.relrowsecurity
	 order by c.relname" | tr -d '\r')

CURRENT=""
CURRENT_KIND=""

restore() {
	[ -z "$CURRENT" ] && return 0
	case "$CURRENT_KIND" in
	disable) psql_su "alter table $CURRENT enable row level security;" >/dev/null 2>&1 ;;
	force) psql_su "alter table $CURRENT force row level security;" >/dev/null 2>&1 ;;
	policy) psql_su "select rls_reapply_all('bokarn_app');" >/dev/null 2>&1 ;;
	esac
	CURRENT=""
	CURRENT_KIND=""
}
trap 'restore; rm -f "$BIN"; exit 130' INT TERM

# Long enough for the pool test's 200 goroutines on a slow runner, short enough
# that a deadlock is reported rather than waited on.
SUITE_TIMEOUT=${BOKARN_MUTATION_SUITE_TIMEOUT:-4m}

echo "Baseline: the suite must pass before anything is mutated."
if ! "$BIN" -test.count=1 -test.timeout="$SUITE_TIMEOUT" >/dev/null 2>&1; then
	echo "  FAILED — fix the suite before mutation testing." >&2
	exit 1
fi
echo "  ok"
echo

survivors=0
mutations=0

for table in $TABLES; do
	for kind in disable force policy; do
		case "$kind" in
		disable) sql="alter table $table disable row level security;" ;;
		force) sql="alter table $table no force row level security;" ;;
		policy) sql="drop policy tenant_select on $table;" ;;
		esac

		if ! psql_su "$sql" >/dev/null 2>&1; then
			echo "  SKIP     $table ($kind) — could not apply"
			continue
		fi
		CURRENT="$table"
		CURRENT_KIND="$kind"
		mutations=$((mutations + 1))

		# failfast: one noticing test is proof enough, and every mutation here
		# is expected to be noticed — so this is the common path, not the edge.
		"$BIN" -test.count=1 -test.failfast \
			-test.timeout="$SUITE_TIMEOUT" >/dev/null 2>&1
		status=$?

		# A Go test binary exits 0 when everything passed and 1 when a test
		# failed. Anything else is a panic or a timeout, which is neither a kill
		# nor a survivor — the suite did not answer the question, and pretending
		# it did would report a hang as proof of coverage.
		case "$status" in
		0)
			echo "  SURVIVED $table ($kind) — the suite does not test this"
			survivors=$((survivors + 1))
			;;
		1)
			echo "  KILLED   $table ($kind)"
			;;
		*)
			echo "  ERROR    $table ($kind) — suite exited $status" >&2
			restore
			exit 1
			;;
		esac

		restore
	done
done

echo
echo "$mutations mutations, $survivors survivors"
if [ "$survivors" -gt 0 ]; then
	echo "A surviving mutation means the isolation tests would pass with that" >&2
	echo "protection removed. Add a test that crosses the boundary." >&2
	exit 1
fi
echo "Every mutation was detected."
