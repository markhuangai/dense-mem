# Ontology receipt recheck study

[Issue #657](https://github.com/markhuangai/dense-mem/issues/657) tracks the
experiment. The workflow materializes archived runtime `214fee2f` and
PostgreSQL study fixtures from `57e6cf41` on GitHub-hosted runners. The
patch and SQL in this directory are inputs to that disposable checkout.
The application build and migration discovery paths never read them.

The original definition query combines explicit dependencies with lexical
hits. The candidate records successful post-publication assessments, the
inputs they read, and whether every source assignment was fixed. New
canonical and catalog writes stamp their inputs with PostgreSQL transaction
IDs. The certification predicate uses the recorded snapshot, the active
receipt links and live claims. Modified inputs continue to enqueue explicit,
scoped rechecks. The original full reader still decides whether to requeue or
publish. Missing, failed, ambiguous and mixed receipts use ordinary selection.

On a paused private clone, the additive migration creates the receipt,
input and change tables and their triggers with a five-second lock timeout.
It does not rewrite historical receipts or canonical Evidence. The existing
per-team turn advances backfill in transactions of at most 12 assessment
receipts, with completion recorded by receipt ID and the original reader
validating eligibility for future pruning. The schema is confined to the
study clone. Keep the clone paused and drained before replacing its study
image. An incompatible image leaves the clone private.

## Hosted verification

A normal push of `perf/ontology-receipt-invalidation` runs correctness, scale,
and matched-drain jobs on GitHub-hosted PostgreSQL 18. The first two jobs
gate the matched five-dataset shapes. Every timed arm includes clone setup
that differs by implementation, incremental backfill, manager correction,
full coverage, pause and drain. The fixed-schedule correctness arms match
provider HTTP inputs and Recall order. They also run the original freshness
reader against every omitted source before counting that skip.

The scale matrix independently varies catalog size at fixed Evidence and
examines disjoint changes, narrow and common lexical overlaps, and a
required definition change. The latter retains validation. The reports
include visited source rows from PostgreSQL plans, provider attempts and
tokens, actual requeues and all paired drain and correction times. A slowdown in at least
three pairs with a median beyond the control's median absolute deviation is
repeatable. A gate of `advance: false` with complete evidence means hold. Missing or failed
jobs leave the evidence incomplete. Keep the run URL, source and patch
hashes, template digests and comparison artifact.

## Private observation after a passing hosted comparison

1. Download `ontology-receipt-study-decision` from the exact successful run.
   Verify `evidence_complete` and `advance` in its `comparison.json`.
   Materialize the matching revision with
   `python3 tests/eval/ontology-receipts/prepare.py private "$private_tree"`.
2. In that checkout, build the local production image with the normal
   Dockerfile and pass `--build-arg IMAGE_REVISION=214fee2f8d3ac53333857888dcdd2ce56a367fa9`.
   Apply the label `io.dense-mem.ontology-receipt-study=<runtime_patch_sha256>`
   from `ontology-receipt-study-identity.json`. Set `EVAL_IMAGE` to the
   resulting immutable Docker image ID. Build the runner with
   `go build -trimpath -o "$private_tree/eval-runner" ./cmd/eval-runner`
   and set `EVAL_RUNNER_BINARY` to that absolute path.
3. Run the private seed entry point with the dedicated evaluation settings
   loaded from their private file.

   ```bash
   node --env-file="$private_env" tests/eval/scripts/receipt-trial.mjs \
     "$hosted_run" "$comparison_json" "$transfer_receipt" "$inactive_checkpoint" "$private_output"
   ```

The entry point checks the hosted decision, the local image label and the
archived transfer identity. It opens the inactive origin volume read-only
and copies it to a separately owned volume. It verifies the canonical
13,810-Evidence corpus, starting queue counts and terminal attempts before
enabling maintenance. The same model, eight worker slots and provider
cooldown apply. The first progress sample starts the measured hour. It
records five-minute samples and pauses and drains within its reserved
budget. It preserves the protected log before cleanup on incomplete exits
and records an unavailable capture explicitly. The original five builders keep their own volumes and runtime
identities. The resulting clone and logs stay private. The entry point
does not call the exporter or publisher.

For log-only review, retain the private container stream and filter by
`run_id`. Join `ontology maintenance discovery`, `ontology receipt lexical skip`
and `ontology receipt discovery rejection` by the run and receipt
identities. Reassemble DEBUG chunks of the same payload hash in index
order, check the hash and capture outcome, then compare its topic and
receipt with the INFO outcomes and provider exchange timestamps.
An output without a complete capture carries its explicit omission state.
Compare the measured progress with the archived 201 organized sources in
3,597.73 seconds. Use provider latency, cooldown events and competing load
to judge comparability. A partial or materially confounded result is
inconclusive.
