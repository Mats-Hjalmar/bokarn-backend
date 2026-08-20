#!/usr/bin/env bash
# Exchanges a dev password for a Kratos session token and prints it, so the
# smoke tests are copy-pasteable:
#
#   TOKEN=$(./scripts/login.sh admin@storsand.example)
#
# Uses the API-type login flow, which returns the token in the response body
# rather than setting a browser cookie.
set -euo pipefail

email=${1:?usage: login.sh <email> [password]}
# Local Docker stack only; never valid in any deployed environment.
password=${2:-${BOKARN_DEV_PASSWORD:-local-dev-only-not-a-real-password}}
KRATOS_STAFF_URL=${KRATOS_STAFF_URL:-http://localhost:4733}

flow=$(curl -sf "$KRATOS_STAFF_URL/self-service/login/api" | jq -r .id)

response=$(curl -sf -X POST \
	"$KRATOS_STAFF_URL/self-service/login?flow=$flow" \
	-H 'Content-Type: application/json' \
	-d "$(jq -n --arg id "$email" --arg pw "$password" \
		'{method: "password", identifier: $id, password: $pw}')")

token=$(jq -r '.session_token // empty' <<<"$response")
if [ -z "$token" ]; then
	echo "login failed for $email:" >&2
	jq -r '.ui.messages[]?.text // .error.message // .' <<<"$response" >&2
	exit 1
fi

printf '%s' "$token"
