#!/usr/bin/env bash
set -euo pipefail

image="${SUPABASE_POSTGRES_IMAGE:-public.ecr.aws/supabase/postgres:17.11.0.006}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
name="leaflet-migrations-$$"

for wait in 0 10 20 40; do
	sleep "$wait"
	if docker image inspect "$image" >/dev/null 2>&1 || docker pull --quiet "$image" >/dev/null; then
		break
	fi
	if [[ $wait -eq 40 ]]; then
		echo "could not pull $image" >&2
		exit 1
	fi
done

docker run --detach --rm --name "$name" --env POSTGRES_PASSWORD=postgres "$image" >/dev/null
trap 'docker rm --force "$name" >/dev/null 2>&1 || true' EXIT

sql() {
	local user="$1"
	shift
	docker exec --interactive --env PGPASSWORD=postgres --env PGOPTIONS="-c client_min_messages=warning" "$name" \
		psql --no-psqlrc --quiet --set ON_ERROR_STOP=1 --host 127.0.0.1 --username "$user" --dbname postgres "$@" --file -
}

for _ in $(seq 120); do
	if echo "select 1" | sql postgres --output /dev/null 2>/dev/null; then
		break
	fi
	sleep 1
done
echo "select 1" | sql postgres --output /dev/null

for seed in "$root"/scripts/migrations/seed/*.sql; do
	version="${seed##*/}"
	if ! compgen -G "$root/supabase/migrations/${version%.sql}_*.sql" >/dev/null; then
		echo "scripts/migrations/seed/$version is named for no migration" >&2
		exit 1
	fi
done

sql supabase_admin <"$root/scripts/migrations/services.sql"
for f in "$root"/supabase/migrations/*.sql; do
	echo "${f##*/}"
	sql postgres --single-transaction --output /dev/null <"$f"
	seed="$root/scripts/migrations/seed/$(basename "$f" | cut -d_ -f1).sql"
	if [[ -f "$seed" ]]; then
		echo "  seed/${seed##*/}"
		sql postgres --single-transaction --output /dev/null <"$seed"
	fi
done
for f in seeded.sql test.sql access.sql; do
	echo "$f"
	sql postgres --output /dev/null <"$root/scripts/migrations/$f"
done
echo realtime.sql
sql supabase_admin --output /dev/null <"$root/scripts/migrations/realtime.sql"
echo "Every migration applies, and the checks pass."
