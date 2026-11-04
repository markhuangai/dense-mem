# Entity identity characterization fixtures

The deterministic provider fixtures isolate real assessment validation, commit
input construction, PostgreSQL commit, graph reads and trace provenance. They
characterize the identity owner; they do not measure live model behavior.

The commit-input owner coalesces duplicate groundings by evidence ID, span and
Entity kind. Distinct spans and fragments remain separately addressable.
PostgreSQL creates an Entity for each accepted `create` decision and validates
server-issued IDs for `reuse`. Homonyms and different Entity kinds stay distinct.

The integration cases inspect durable resolution events, graph edges, exact
support occurrences/spans/quotes and bounded catalog candidates. Shared readers
B/C and foreign/private actors exercise visibility and rollback boundaries.
The fixtures do not authorize name-only merging or rewriting canonical data.

Run the focused cases after plan conformance:

```bash
go test ./internal/remember/service -run '^TestIdentityCharacterization' -count=1
env -u DATABASE_URL DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 \
  DENSE_MEM_REQUIRE_POSTGRES_TESTS=1 \
  go test -tags integration ./internal/knowledge/postgres \
  -run '^TestRememberIdentityCharacterization' -count=1 -timeout=30m
```

The Compose-only primitive case requires the harness-supplied `DATABASE_URL`;
it is separate from these disposable PostgreSQL tests. Controlled provider
answers establish server policy and provenance, not historical defect origin.
