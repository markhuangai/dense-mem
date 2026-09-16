# Within-batch entity identity characterization

This report records the controlled characterization for issue #428. It is
research evidence, not a production identity policy. The tests use deterministic
provider responses only to isolate the real assessment validation, semantic
commit, graph read, and trace provenance paths.

## Question

Does one Remember batch preserve one intended Entity identity when several
evidence items or mentions refer to it, while retaining distinct homonyms,
different Entity kinds, ownership boundaries, and explicit server-issued IDs?

## Retained staging observation

The 2026-09-15 prerelease evaluation reported the same tokenized Cedar and
Harbor names under three Entity IDs each in one structured request. It also
showed that the suspected Pine two-hop path remained connected. The original
provider exchange is not retained in this repository; that observation is
therefore treated as a candidate, not a reproduced production result.

## Controlled matrix

| Case | Controlled input | Required observation |
| --- | --- | --- |
| Repeated mention | Same name and kind at two distinct spans | The resolver/commit path records the selected IDs and does not silently merge spans. |
| Same grounding | Same evidence/span/kind key | The service-level commit-input owner coalesces the grounding. |
| Explicit reference | Existing `known_entity_id` | The selected server ID is retained even when same-name candidates exist. |
| Homonym | Same name and kind, different identity candidates | Candidates remain separate and selectable. |
| Kind distinction | Same name with different Entity kinds | Kind filtering does not conflate identities. |
| Provenance | Every stored Relationship cites submitted evidence | Trace returns the exact occurrence, span, and quote. |
| Visibility | Same-team second profile and different team | Same-team reads see the result; another team has no candidates or graph data. |

## Authoritative path

```text
controlled assessor response
  -> AssessSynchronousRemember
  -> Build/commit input validation
  -> CommitRememberWithEmbeddings (one PostgreSQL transaction)
  -> entity_resolution_events + entity_records + relationships
  -> graph and trace read models
```

The relevant identity owner combines duplicate groundings by evidence ID,
span, and Entity kind. PostgreSQL creates a new Entity for each accepted
`create` resolution and checks exact references for `reuse`. The integration
test reads the durable resolution events, graph edges, trace supports, and
bounded catalog candidates directly.

## Results

The focused service characterization passed three tests. It showed that
duplicate groundings for the same evidence, span, and kind coalesce before the
commit input is built, while two distinct Alpha spans remain two groundings.
Repeated names in different evidence fragments also remain separately
addressable. Same-name candidate groups retain both candidate IDs.

The disposable PostgreSQL characterization passed three tests. The controlled
commit produced seven durable resolution events: two Cedar/project identities
for the repeated create decisions, one Cedar/product identity for the kind
control, two Harbor/product identities for the create plus explicit known-ID
reuse, and two Pine/project reuse events for one identity. The graph contained
the three expected edges, including the connected Harbor-to-Pine control. Each
relationship trace returned its submitted quote and exact span
(`Cedar uses Harbor.`, `Cedar uses Pine.`, or `Harbor uses Pine.`), occurrence
ID, evidence fragment, and owner profile.

Same-team profiles B and C saw the two shared Harbor candidates; the other team
saw none, and an entity in the credential-private space was absent from the
shared catalog. A cross-team exact reference failed as
`ErrRememberExactReferenceStale`, and the transaction left zero ingest,
resolution-event, or relationship rows.

These controls do not reproduce an unintended server-side identity merge or a
server-side loss of provenance. The owner preserves the assessor's explicit
create/reuse decisions: repeated names without an explicit identity remain
separate, while exact references reuse the selected ID. No production remedy is
implemented by this change.

## Plan conformance

The pre-test independent plan audit returned `conformant` with no findings.
Auditor: `gpt-5.6-terra` at maximum reasoning effort. Audit base:
`d993e24905253b4694a4bf4375a1241c2b36e145`. The audited branch contained the
characterization fixtures, report, PostgreSQL module requirements, Compose
guard, and Playwright runtime alignment.

Commands used:

```bash
go test ./internal/remember/service -run 'TestIdentityCharacterization' -count=1
go test ./internal/remember/... ./internal/knowledge/postgres -count=1
env -u DATABASE_URL DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 go test -timeout=30m -tags=integration ./internal/remember/... ./internal/knowledge/postgres -count=1
env -u DATABASE_URL ./scripts/e2e.sh synchronous_write
env -u DATABASE_URL ./scripts/ci-check.sh
```

The focused service run passed three tests. The focused disposable PostgreSQL
run passed the three characterization tests in 22.879 seconds. The complete
Remember/knowledge run passed with 831.248 seconds for the PostgreSQL package,
zero failed tests, and the Compose-only test skipped because the command
intentionally unsets `DATABASE_URL`. The registered Compose E2E path still runs
that test when it supplies `DATABASE_URL`.

The synchronous-write E2E passed its architecture/controller UAT and all four
Chromium/mobile Playwright specs. Repository CI passed architecture, registry,
coverage-policy, browser (156 tests), proxy (19 tests), Go, and complete
coverage checks; complete Go coverage was 90.1% (28,969/32,160 statements).

The frozen integration command without an explicit timeout cannot complete this
repository's 112 serial disposable-container tests within Go's default
10-minute package timeout. The `-timeout=30m` invocation above runs the same
packages and test set to completion. The repository's existing Compose harness
branch now skips only when its required `DATABASE_URL` is absent; this keeps the
disposable command deterministic without changing the Compose scenario.

Post-audit support paths are `go.mod`, `go.sum`,
`internal/knowledge/postgres/remember_primitives_compose_e2e_integration_test.go`,
and `scripts/e2e-host-controller-runtime.sh`. The runtime image now matches the
Playwright 1.63.0 version pinned by `web/package-lock.json`.
The two testcontainers requirements restore the versions already used by the
repository's integration fixtures. No temporary module overlay is required.

## Verification limits

- Provider output is deterministic test input and does not measure model
  behavior frequency.
- The retained staging observation has no original provider exchange, so the
  controlled tests establish owner behavior rather than regression origin.
- No name-only merge, backfill, graph rewrite, migration, or live-data write is
  part of this characterization.
