#!/usr/bin/env bash
# Applies every migration in supabase/migrations, oldest first, to a new
# database, as the app does to each person's own project, then runs the
# checks in scripts/migrations/seeded.sql, test.sql, access.sql and
# realtime.sql on it.
# The database is a throwaway container of the supabase/postgres image,
# which hosted projects run, made and removed here: only Docker is needed.
#
#   scripts/check-migrations.sh
#
# The image alone has an early auth.users and an empty storage schema, which
# Supabase's Auth and Storage services fill in a project: first
# scripts/migrations/services.sql adds what the migrations use of them.
# Each migration then runs in a transaction of its own, as the app sends it,
# and as postgres, who in a Supabase project is not a superuser. A
# project's tables are not empty when a migration comes, so rows go in
# along the way: scripts/migrations/seed/<version>.sql, where there is one,
# runs right after the migration of that version, and seeded.sql checks
# what the later migrations made of its rows.
# SUPABASE_POSTGRES_IMAGE picks another image.
set -euo pipefail

image="${SUPABASE_POSTGRES_IMAGE:-public.ecr.aws/supabase/postgres:17.11.0.006}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
name="leaflet-migrations-$$"

# The registry turns pulls away for a while when too many come from one
# address (toomanyrequests), as from CI's shared runners: the image is
# pulled first, asked for again over a minute and more before giving up.
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

# psql in the container as the given user, on the file read from stdin.
sql() {
	local user="$1"
	shift
	docker exec --interactive --env PGPASSWORD=postgres --env PGOPTIONS="-c client_min_messages=warning" "$name" \
		psql --no-psqlrc --quiet --set ON_ERROR_STOP=1 --host 127.0.0.1 --username "$user" --dbname postgres "$@" --file -
}

# The image sets the database up on a server that listens on its socket
# only, then starts the real one: it is up when a query over TCP goes
# through.
for _ in $(seq 120); do
	if echo "select 1" | sql postgres --output /dev/null 2>/dev/null; then
		break
	fi
	sleep 1
done
echo "select 1" | sql postgres --output /dev/null

# A seed named for no migration would never run.
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
