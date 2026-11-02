#!/usr/bin/env bash
# Applies every migration in supabase/migrations, oldest first, to a new
# database, as the app does to each person's own project, then runs the
# checks in scripts/migrations/test.sql on it. The database is a throwaway
# container of the supabase/postgres image, which hosted projects run, made
# and removed here: only Docker is needed.
#
#   scripts/check-migrations.sh
#
# The image alone has an early auth.users and an empty storage schema, which
# Supabase's Auth and Storage services fill in a project: first
# scripts/migrations/services.sql adds what the migrations use of them.
# Each migration then runs in a transaction of its own, as the app sends it,
# and as postgres, who in a Supabase project is not a superuser.
# SUPABASE_POSTGRES_IMAGE picks another image.
set -euo pipefail

image="${SUPABASE_POSTGRES_IMAGE:-public.ecr.aws/supabase/postgres:17.11.0.006}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
name="leaflet-migrations-$$"

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

sql supabase_admin <"$root/scripts/migrations/services.sql"
for f in "$root"/supabase/migrations/*.sql; do
	echo "${f##*/}"
	sql postgres --single-transaction --output /dev/null <"$f"
done
echo "test.sql"
sql postgres --output /dev/null <"$root/scripts/migrations/test.sql"
echo "Every migration applies, and the checks pass."
