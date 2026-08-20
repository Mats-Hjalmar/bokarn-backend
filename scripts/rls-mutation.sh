#!/usr/bin/env bash
# Mutation-tests the isolation suite.
#
# A passing RLS test proves nothing on its own: it passes identically when the
# policies are enforcing and when they are absent but no test actually crosses a
# tenant boundary. So this disables the protection one table at a time, re-runs
# the suite, and asserts it FAILS. A mutation the suite does not notice is a
# hole in the tests, reported as a survivor.
#
# The suite is compiled once and Postgres is reached directly rather than
# through `docker compose exec`; doing either per mutation turns a twenty-second
# check into a ten-minute one, and a check nobody runs protects nothing.
#
# Every mutation is restored, including on interrupt.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

PGHOST=${BOKARN_DB_HOST:-127.0.0.1}
PGPORT=${BOKARN_DB_PORT:-1438}
SUPERUSER=${BOKARN_DB_SUPERUSER:-postgres}
export PGPASSWORD=${BOKARN_DB_SUPERUSER_PASSWORD:-postgres}

psql_su() {
	psql -h "$PGHOST" -p "$PGPORT" -U "$SUPERUSER" -d bokarn \
		-X -q -v ON_ERROR_STOP=1 -c "$1"
}

BIN=$(mktemp -t bokarn-e2e)
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

echo "Baseline: the suite must pass before anything is mutated."
if ! "$BIN" -test.count=1 >/dev/null 2>&1; then
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

		if "$BIN" -test.count=1 >/dev/null 2>&1; then
			echo "  SURVIVED $table ($kind) — the suite does not test this"
			survivors=$((survivors + 1))
		else
			echo "  KILLED   $table ($kind)"
		fi

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
