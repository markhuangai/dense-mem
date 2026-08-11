#!/bin/sh
# docker-entrypoint.sh
# The application builds a safely escaped PostgreSQL DSN from component
# environment variables. This wrapper only preserves signal/exit semantics.
set -eu
exec "$@"
