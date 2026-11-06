# Evaluation client measurements

Issue #680 reuses the paired runner, readiness and source receipts from #660/#668,
the alias scorer from #665, the negative cohort from #666, and PR #697's provider
capture, ontology reconciliation and resource evidence. The remaining changes
measure client operations and preserve unsuccessful work. Provider calls, token
usage and price availability continue to come from their existing receipts;
client attempts are never added to those provider totals. Broader unified cost
reporting belongs to #685. No new live 1k campaign is required for this change.

## Capture and replay

Live import and retrieval modes with `--out` create a private append-only
`execution_journal.jsonl`. `execution_measurements.json` identifies its schema,
capture, run, input seed and exact suite, planned source/case IDs, final status,
outcome counts and hashes for the journal, traces, mapping, suite, run configuration, summary and retrieval scores. The journal records operation and HTTP-attempt
intent before dispatch, terminal outcomes, retry delays and successful trace
identities. Its writes are synchronized and flushed before proceeding. A capture
write failure stops further work and returns an error.

Source/case work is distinct from setup operations, client HTTP attempts,
durable source receipts and provider exchanges. Reusing a verified imported
source records `reused`, without inventing a client/provider dispatch. A scheduled
item without an operation is `not_dispatched`; a started item without a terminal
record is `unfinished`. Intent proves a request may have been dispatched, not
that a server received it. Failed and cancelled predecessors remain in the
journal when a later attempt succeeds. The existing six-attempt bound, transient
classification, idempotency and paired stage retry barriers remain authoritative.

Successful traces are appended immediately. A later failure keeps those traces
and leaves the manifest incomplete. New traces carry `measurement_capture_id`,
`client_operation_id`, nullable `client_elapsed_ms` and `client_phase`.
`client_elapsed_ms` uses monotonic elapsed time from operation entry through
result decoding and includes discovery, coordination waits, retries and backoff
where they occur. Capture overhead can affect this measurement. The original
`latency_ms` is the server Recall-service interval and is never substituted or
added to the client timer. Import/setup, first query and subsequent query phases
stay separate; subsequent query does not certify a warm server cache.

New measured captures require the journal, exact input identities, hashes,
completed planned work and matching trace identities before replay/comparison.
Missing, duplicate and unexpected case traces fail general scoring too. Use a
new live output directory; replay must use a separate directory and the original
`--mapping`. Replay copies measurement evidence without appending to or changing
its source. Legacy captures without measurement provenance remain scoreable;
client timing and attempt coverage are unavailable, rather than zero or inferred
from the server timer. An incomplete measured capture cannot become legacy by
removing its journal.

## Diagnose from logs

Set `LOG_LEVEL=debug` before starting an evaluation. Existing level parsing and
defaults apply. Capture stdout/stderr in the paired stage's `native.log`, or
redirect a direct runner invocation to a private file. Retain the run directory
until validation and review finish. Delete only owned temporary evidence after
its compact receipts have been retained; preserve historical captures.

```bash
jq -R 'fromjson? | select(.run_id == $run)' \
  --arg run "$run_id" "$capture/native.log"
```

INFO events identify run/operation boundaries and outcomes. DEBUG events retain
attempt details and protected admitted exchanges; WARN `evaluation_retry_scheduled`
includes the cause and delay; ERROR operation/run outcomes preserve the failed
case or source. `evaluation_capture_invalid` reports replay validation failure,
expected seed/cohort and the causal missing/mismatched artifact.

Follow `capture_id`, `operation_id` and `attempt_id` through `execution` fields.
Each dispatched request supplies a UUID correlation ID; its observed response ID
is retained with the terminal attempt. For Remember, collect the server's existing
admission-aware logs and join their authenticated team/profile and correlation
IDs to the client journal. Do not attach to the process or query business tables:

```bash
docker logs "$evaluation_server" > "$capture/server.log" 2>&1
jq -R 'fromjson? | select(.correlation_id == $correlation)' \
  --arg correlation "$correlation_id" "$capture/server.log"
```

The server's configured log collector/rotation governs server retention; export
these logs before retiring disposable runtimes. Client files remain in the
private run directory through validation. The client protects its configured and
presented credentials with the existing protector and never captures HTTP
authentication headers. Returned failures use the same protection while retaining
their error chain for classification. Protection that would change execution
identities stops work before the affected journal record is written. Saved
endpoint metadata omits URL user information;
operation and transport diagnostics protect its serialized forms while requests
retain the supplied endpoint and Bearer authentication. Remember payloads and provider credentials remain with
PR #697's server-side protection and initial-security admission rules.

For `ai_exchange_payload`, group by attempt/exchange and direction, order
`payload_chunk` by `index`, require all `count` parts, concatenate `body`, and
verify the protected-body SHA-256. The existing 16 MiB body bound applies. Inspect
`state`, `reason` and `transport_capture_state`; unavailable/interrupted capture
is explicit and cannot establish zero usage or a complete response. A 503 then
success must show the first failed attempt, WARN backoff, second completion and
one completed logical operation. Malformed JSON or cancellation must identify
the affected operation, retain prior successes and leave incomplete work visible.
A missing journal requires restoring the original capture, not retrying ingestion.

Use `eval-runner --mode validate-capture --capture-dir "$capture"` to check saved
execution measurements without dispatching requests or changing artifacts.
The directory must contain a readable `run_config.json` object; empty directories
and invalid configurations fail with protected `evaluation_capture_invalid` logs.
Live runs validate a saved mapping's capture before starting work. The non-paired
monitor also validates imports and baselines when reusing cached results.
Legacy captures retain their existing unavailable measurements.

## Independent scoring verification

Use the pinned reference packages in an isolated environment:

```bash
verification_dir="$(mktemp -d)"
python3 -m venv "$verification_dir/venv"
"$verification_dir/venv/bin/pip" install -r tests/eval/requirements-score-check.txt
go test ./internal/evalharness -run '^TestMeasurementScoreReference$' -count=1 -v \
  > "$verification_dir/reference.log"
"$verification_dir/venv/bin/python" tests/eval/scripts/verify_measurement_scores.py \
  --go-log "$verification_dir/reference.log" --out "$verification_dir/reference.json"
```

Hand-computed examples preserve aliases, grouped alternatives, source coverage,
duplicate result slots, empty results and missing support. Ordinary ranking
calculations are also checked against pinned `ir-measures`/`pytrec-eval-terrier`;
exponential gains are supplied explicitly. Grouped-source recall and displayed
slots are not misrepresented as ordinary TREC document rankings.

Replay both verified historical 991-document/204-case captures using the existing
`--traces`/`--mapping` commands in the evaluation guide. Add two matching pairs of
`--original-run DIR --replayed-run DIR` and the frozen `--seed MANIFEST` to the verifier. It requires every unique
case, unchanged per-case scores, the same seed and independently recomputed
aggregates. Record dependency versions and artifact hashes. Offline verification
makes no provider calls and establishes no new live quality, latency or readiness.
