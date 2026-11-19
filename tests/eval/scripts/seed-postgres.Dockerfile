ARG POSTGRES_BASE=pgvector/pgvector:0.8.2-pg18-trixie@sha256:b7337db8fe39d12fe8ecb0003c72680f24479813a744b43154eee6f2eab5a5f3
FROM ${POSTGRES_BASE} AS restore
USER postgres
COPY --chown=postgres:postgres database.dump restore.list seed-restore.sh /tmp/
RUN <<'SH'
set -eu
data=/tmp/seed-pgdata
initdb -D "$data" --username=densemem --auth-local=peer --auth-host=scram-sha-256
cat >"$data/pg_hba.conf" <<'HBA'
local all densemem peer map=eval_seed
host densemem densemem all scram-sha-256
HBA
printf '%s\n' 'eval_seed postgres densemem' >>"$data/pg_ident.conf"
pg_ctl -D "$data" -o "-c listen_addresses= -k /tmp" -w start
export PGHOST=/tmp
createdb --username=densemem densemem
sh -e /tmp/seed-restore.sh
test "$(psql -XAt --username=densemem --dbname=densemem -c 'SELECT count(*) FROM pg_authid WHERE rolcanlogin AND rolpassword IS NOT NULL')" = 0
pg_ctl -D "$data" -m fast -w stop
pg_controldata "$data" | grep -E '^Database cluster state: +shut down$'
SH

FROM ${POSTGRES_BASE}
LABEL org.opencontainers.image.source="https://github.com/markhuangai/dense-mem" \
      io.dense-mem.eval.seed-format="postgres-v2"
COPY --from=restore --chown=postgres:postgres /tmp/seed-pgdata/ /var/lib/postgresql/18/docker/
COPY --chmod=0755 seed-postgres-entrypoint.sh /usr/local/bin/
COPY seed.json mapping.json NOTICE.md /seed/
ENTRYPOINT ["seed-postgres-entrypoint.sh"]
CMD ["postgres"]
