#!/usr/bin/env bash
set -euo pipefail

# The same image, by its digest, from Docker Hub or else from Amazon's registry, whose
# anonymous pulls run out of data on the shared runners.
postgres="supabase/postgres:17.11.0.006@sha256:c141fd17ac6a76c4323e90b3aee8ff1a23d2e89e547c2b963cfadc309c079cc2"
if [[ -n "${SUPABASE_POSTGRES_IMAGE:-}" ]]; then
	images=("$SUPABASE_POSTGRES_IMAGE")
else
	images=("docker.io/$postgres" "public.ecr.aws/$postgres")
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
name="leaflet-migrations-$$"

# pulled sets IMAGE to the first of the images there is, or that pulls.
IMAGE=""
pulled() {
	local wait candidate
	for wait in 0 10 20 40; do
		sleep "$wait"
		for candidate in "${images[@]}"; do
			if docker image inspect "$candidate" >/dev/null 2>&1 || docker pull --quiet "$candidate" >/dev/null; then
				IMAGE="$candidate"
				return 0
			fi
		done
	done
	return 1
}
if ! pulled; then
	echo "could not pull ${images[*]}" >&2
	exit 1
fi

docker run --detach --rm --name "$name" --env POSTGRES_PASSWORD=postgres "$IMAGE" >/dev/null
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
