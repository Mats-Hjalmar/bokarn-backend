#!/usr/bin/env bash
# Seeds local dev staff accounts, one administrator per operator plus one
# bokarn platform operator.
#
# The operator a staff member belongs to is carried in the Kratos identity's
# metadata_public.tenant_id — that is what the API reads to pin the tenant, and
# it is writable only through the admin API, so a staff member cannot move
# themselves. metadata_public rather than metadata_admin because only the
# former is returned by the public session endpoint the API calls.
#
# Idempotent: existing identities are updated in place, existing grants left
# alone.
set -euo pipefail

KRATOS_STAFF_ADMIN_URL=${KRATOS_STAFF_ADMIN_URL:-http://auth-staff-admin.bokarn.localhost}
# Local Docker stack only; never valid in any deployed environment.
PASSWORD=${BOKARN_DEV_PASSWORD:-local-dev-only-not-a-real-password}

STORSAND=11111111-1111-1111-1111-111111111111
HAMNVIKEN=22222222-2222-2222-2222-222222222222

psql_app() {
	docker compose exec -T postgres \
		psql -U postgres -d bokarn -v ON_ERROR_STOP=1 -q "$@"
}

# upsert_identity <email> <name> <metadata-json>
upsert_identity() {
	local email=$1 name=$2 metadata=$3 id

	id=$(curl -sf \
		"$KRATOS_STAFF_ADMIN_URL/admin/identities?credentials_identifier=$email" |
		jq -r 'if type == "array" then (.[0].id // "") else (.id // "") end')

	local body
	body=$(jq -n --arg email "$email" --arg name "$name" \
		--argjson metadata "$metadata" --arg password "$PASSWORD" '{
			schema_id: "default",
			traits: { email: $email, name: $name },
			metadata_public: $metadata,
			credentials: { password: { config: { password: $password } } }
		}')

	if [ -z "$id" ]; then
		id=$(curl -sf -X POST "$KRATOS_STAFF_ADMIN_URL/admin/identities" \
			-H 'Content-Type: application/json' -d "$body" | jq -r .id)
		echo "  created $email" >&2
	else
		# PUT replaces the identity, so metadata_public is re-asserted every
		# run and a hand-edited tenant is corrected rather than kept.
		curl -sf -X PUT "$KRATOS_STAFF_ADMIN_URL/admin/identities/$id" \
			-H 'Content-Type: application/json' -d "$body" >/dev/null
		echo "  updated $email" >&2
	fi

	printf '%s' "$id"
}

# grant_admin <tenant-uuid> <identity-id>
#
# The API provisions the users row itself on first login, but the role grant
# cannot be inferred from a session — so both are written here, which also
# means a fresh database is usable without anyone logging in first.
grant_admin() {
	local tenant=$1 identity=$2
	psql_app <<SQL
insert into users (tenant_id, external_user_id)
values ('$tenant', 'staff:$identity')
on conflict (external_user_id) do nothing;

insert into user_roles (tenant_id, user_id, role_id)
select '$tenant', u.id, r.id
  from users u, roles r
 where u.external_user_id = 'staff:$identity'
   and r.tenant_id = '$tenant'
   and r.name = 'Administratör'
on conflict do nothing;
SQL
}

echo "Seeding staff identities..."

id=$(upsert_identity "admin@storsand.example" "Storsand Admin" \
	"$(jq -n --arg t "$STORSAND" '{tenant_id: $t}')")
grant_admin "$STORSAND" "$id"

id=$(upsert_identity "admin@hamnviken.example" "Hamnviken Admin" \
	"$(jq -n --arg t "$HAMNVIKEN" '{tenant_id: $t}')")
grant_admin "$HAMNVIKEN" "$id"

# The platform operator carries no tenant_id at all: it is not a member of any
# campsite, and the staff verifier refuses a session without one.
upsert_identity "platform@bokarn.example" "bokarn Platform" '{"platform": true}' >/dev/null

echo
echo "Dev accounts (password: $PASSWORD)"
echo "  admin@storsand.example     administrator at Storsand"
echo "  admin@hamnviken.example    administrator at Hamnviken"
echo "  platform@bokarn.example    bokarn platform operator, no tenant"
