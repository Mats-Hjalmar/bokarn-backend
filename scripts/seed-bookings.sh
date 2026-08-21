#!/usr/bin/env bash
# Creates a handful of demo bookings through the real API.
#
# Not a SQL seed, deliberately. A booking is a quote, a frozen breakdown, an
# assigned pitch, an access token and an outbox message, and writing those by
# hand in SQL would mean reimplementing the booking domain in a file nobody
# tests. Going through the API means the demo data is indistinguishable from
# real data — and that running this is itself a smoke test of the whole path.
#
# Idempotent: each booking carries a fixed idempotency key, so a second run
# returns the same references instead of filling the calendar twice.
#
#   ./scripts/seed-bookings.sh
#   API_BASE=http://storsand.api.bokarn.localhost ./scripts/seed-bookings.sh
set -euo pipefail

API_HOST=${API_HOST:-api.bokarn.localhost}
OPERATOR=${OPERATOR:-storsand}
API="http://${OPERATOR}.${API_HOST}/api/v1"

if ! curl -sf -m 5 "http://${API_HOST}/api/v1/healthz" >/dev/null; then
	echo "the API is not answering on http://${API_HOST}" >&2
	echo "start the stack with 'make dev' from the repository root" >&2
	exit 1
fi

# day <offset> — an offset may be negative, so the sign is normalised onto it
# rather than hard-coded as +: date -v+-1d is not a thing.
day() {
	local offset=$1
	case "$offset" in
	-*) ;;
	*) offset="+$offset" ;;
	esac

	if date -v+1d +%F >/dev/null 2>&1; then
		date -v"${offset}d" +%F
	else
		date -d "$offset days" +%F
	fi
}

# The categories differ per operator, so each has its own list rather than one
# list that silently skips half its rows on the campsite that has no pitches.
#
# arrival-offset, nights, category, adults, children (birth years), pets,
# given name, surname, country, locale
case "$OPERATOR" in
storsand)
	bookings=(
		"1|3|pitch_el|2|2014|1|Anna|Lindqvist|SE|sv"
		"2|4|pitch_el|2||0|Johan|Bergström|SE|sv"
		"3|7|stuga4|2|2016,2019|0|Mette|Sørensen|DK|en"
		"5|2|pitch_el|2||1|Klaus|Hoffmann|DE|de"
		"6|5|stuga6|4|2011|0|Ingrid|Vikström|SE|sv"
		"0|2|pitch_el|1||0|Pekka|Laine|FI|en"
		"-1|3|stuga4|2||0|Nora|Halvorsen|NO|en"
	)
	;;
hamnviken)
	bookings=(
		"1|4|stuga4|2|2015|0|Elin|Nyberg|SE|sv"
		"2|3|stuga4|2||0|Bram|Jansen|NL|en"
		"4|6|stuga4|2|2013,2017|0|Sofia|Alanko|FI|en"
	)
	;;
*)
	echo "no demo bookings defined for operator $OPERATOR" >&2
	exit 1
	;;
esac

created=0
replayed=0

for spec in "${bookings[@]}"; do
	IFS='|' read -r offset nights category adults births pets given surname \
		country locale <<<"$spec"

	arrival=$(day "$offset")
	departure=$(day "$((offset + nights))")

	children='[]'
	if [ -n "$births" ]; then
		children=$(IFS=','
		set -- $births
		printf '['
		sep=''
		for year in "$@"; do
			printf '%s{"date_of_birth":"%s-06-15"}' "$sep" "$year"
			sep=','
		done
		printf ']')
	fi

	quote=$(curl -sf -X POST "$API/quotes" -H 'Content-Type: application/json' \
		-d "{\"category_code\":\"$category\",\"arrival\":\"$arrival\",
		     \"departure\":\"$departure\",\"adults\":$adults,
		     \"children\":$children,\"pets\":$pets,\"vehicles\":1,
		     \"campaign_code\":\"\"}" || true)

	quote_id=$(printf '%s' "$quote" | jq -r '.id // empty')
	if [ -z "$quote_id" ]; then
		echo "  skipped ${surname}: no price for $arrival ($category)" >&2
		continue
	fi

	# Fixed key per booking, so re-running does not double-book the calendar.
	key="seed-${OPERATOR}-${surname}-${arrival}"
	email="${given}.${surname}@example.$(printf %s "$country" | tr 'A-Z' 'a-z')"

	response=$(curl -s -o /tmp/bokarn-seed-booking.json -w '%{http_code}' \
		-X POST "$API/bookings" \
		-H "Idempotency-Key: $key" -H 'Content-Type: application/json' \
		-d "{\"quote_id\":\"$quote_id\",\"hold_token\":\"\",
		     \"category_code\":\"$category\",\"arrival\":\"$arrival\",
		     \"departure\":\"$departure\",\"adults\":$adults,
		     \"children\":$children,\"pets\":$pets,\"vehicles\":1,
		     \"campaign_code\":\"\",
		     \"guest\":{\"given_names\":\"$given\",\"surname\":\"$surname\",
		                \"email\":\"$email\",\"phone\":\"+4670$RANDOM$RANDOM\",
		                \"country_of_residence\":\"$country\"},
		     \"locale\":\"$locale\",\"marketing_consent\":true,\"notes\":\"\"}")

	reference=$(jq -r '.reference // empty' /tmp/bokarn-seed-booking.json)
	case "$response" in
	201)
		created=$((created + 1))
		printf '  %-8s %-10s %s → %s  %s\n' \
			"$reference" "$category" "$arrival" "$departure" "$surname"
		;;
	200)
		replayed=$((replayed + 1))
		printf '  %-8s %-10s already booked\n' "$reference" "$category"
		;;
	*)
		echo "  failed ${surname}: HTTP $response $(
			jq -r '.detail // .title // empty' /tmp/bokarn-seed-booking.json
		)" >&2
		;;
	esac
done

rm -f /tmp/bokarn-seed-booking.json

# Anybody who arrived before today is checked in, so the demo opens with guests
# on site as well as guests due. Leaving every booking in 'confirmed' would show
# a reception screen with three empty lists and one arrival, which is not what a
# campsite looks like at eleven in the morning.
#
# Strictly before today: today's arrival is deliberately left for whoever is
# demonstrating to check in themselves.
token=$(./scripts/login.sh "admin@${OPERATOR}.example" 2>/dev/null || true)
if [ -n "$token" ]; then
	arrived=$(curl -sf "$API/admin/bookings?state=confirmed" \
		-H "Authorization: Bearer $token" |
		jq -r --arg today "$(day 0)" '.[] | select(.arrival < $today) | .id')

	for id in $arrived; do
		curl -sf -o /dev/null -X POST "$API/admin/bookings/$id/check-in" \
			-H "Authorization: Bearer $token" &&
			echo "  checked in $id" >&2
	done
fi

echo
echo "$created created, $replayed already present"
echo "Confirmation emails are in the mail catcher: http://mail.bokarn.localhost"
