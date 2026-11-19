#!/usr/bin/env bash
set -Eeuo pipefail

source /usr/local/bin/docker-entrypoint.sh
docker_setup_env
if [[ "${1:-}" != postgres || "${DATABASE_ALREADY_EXISTS:-}" != true || "$POSTGRES_USER" != densemem || "$POSTGRES_DB" != densemem || -z "$POSTGRES_PASSWORD" ]]; then
	printf '%s\n' 'Invalid seeded PostgreSQL directory or runtime credential configuration' >&2
	exit 1
fi

docker_create_db_directories
if [[ "$(id -u)" == 0 ]]; then
	exec gosu postgres "$0" "$@"
fi

docker_temp_server_start "$@"
trap 'docker_temp_server_stop' EXIT
printf '%s\n%s\n' "$POSTGRES_PASSWORD" "$POSTGRES_PASSWORD" |
	docker_process_sql --quiet --command='\password densemem'
docker_temp_server_stop
trap - EXIT

exec /usr/local/bin/docker-entrypoint.sh "$@"
