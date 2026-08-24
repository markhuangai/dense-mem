# Evaluation

Dense-Mem evaluates retrieval through the production MCP submission and recall
paths. The evaluation corpus, downloaded datasets, runtime databases,
credentials, and full run outputs are local-only ignored artifacts. The
generator, source locks, validation, scoring, and compact baseline evidence are
reviewed with code.

## v2.6 Remember Reliability Exception

This change intentionally does not run the deterministic 1k retrieval
evaluation, the 100-row smoke, or publish an accuracy comparison. The semantic
node model is unchanged, and the acceptance evidence is the complete no-skip
Compose/UAT matrix plus focused real-PostgreSQL/service tests. Those checks prove
the v2.6 request contract, assessor repair, atomic staging/activation,
idempotency, isolation, and rejected-versus-failed status behavior. They do not
measure duplicate-Entity or dropped-hint rates. Later retrieval-quality or
ranking changes restore the normal 1k gate.

## Image Boundary And Tool Surface

The release Docker target is `production`; its binary is compiled without the
`evaluation` build tag and contains no evaluation MCP tools. The local
`evaluation` target adds only:

- `eval_list_knowledge_refs`
- `eval_run_dream_cycle`
- `eval_run_recall_case`

There is no runtime evaluation-mode setting. The removed manifest/item/feedback
event/scoring `eval_*` tools had no current harness callers. The committed
`examples/docker-compose.evaluation.yml` builds the evaluation target, binds
ports to loopback, stores state under the ignored evaluation runtime, and loads
the ignored repository-root `.env` by default. When overriding
`DENSE_MEM_EVAL_ENV_FILE`, use an absolute path; the monitor canonicalizes a
relative input before passing it to Compose.

## 100-Row Smoke

`cmd/eval-seedgen --preset local_eval_100` produces the stable
`local_eval_100_v2` identity: 100 corpus rows and 25 scored sanity cases. It
checks the evaluation image, real remember/status import, knowledge mapping,
recall-case execution, scoring, and artifact identity without running the 1k
release evaluation.

The smoke run requires all 100 latest placements/fragments to be terminal with
no failed, pending, or quarantined rows; all 25 cases must score; unmapped refs,
bad rank one, and average bad@k must be zero. It may reuse the public gate's
recall@k and required-rank-one floors as smoke thresholds, but the output must
be labeled smoke-only. `dataset_identity.json` records seed/suite hashes,
runner hash, server image ID, team, and provider fingerprints. This smoke never
replaces the deterministic 1k gate when a change affects retrieval quality.
Use `IMPORT_CONCURRENCY=5` for the smoke run; the full harness still supports
up to 10 concurrent imports.

## Current Retrieval Evaluation

The release retrieval gate is the public six-axis 1k corpus:

| Axis | Dataset | Purpose |
| --- | --- | --- |
| scifact | BEIR SciFact | Scientific claim/document retrieval. |
| msmarco | BEIR MS MARCO | Web-passage retrieval. |
| hotpotqa | BEIR HotpotQA | Public passage retrieval. |
| musique | MuSiQue answerable dev | Multi-hop Relationship retrieval. |
| qasper | QASPER | Paper QA evidence retrieval. |
| longmem_oracle | LongMemEval-S cleaned | Long-memory chat recall. |

The historical V1 seed cannot enter the current submission API because it lacks
the required Relationship properties. The V2 seed is a
source-locked derivative of a declared V1 cohort: every retained evidence item
is identical to V1, and V2 adds only the required `relationships` property.

~~~text
V1 source-locked cohort
  -> declared relationship-ineligible exclusions
  -> retain evidence and source metadata unchanged
  -> add logical relationships array with evidence_indices
  -> real Go relationship validation
  -> retained-evidence identity comparison
  -> live write-time scanner, assessor, and retrieval evaluation
~~~

## V1-to-V2 Seed Comparison Gate

No live evaluation begins until the generated V2 seed has passed both
validation and a detailed V1/V2 comparison.

The comparison reports, without exposing raw restricted corpus content:

| Check | Required result |
| --- | --- |
| Source lock | V1 manifest/hash matches the approved source. |
| Cohort | A committed cohort lock binds the frozen source and filtered cohort hashes. It explicitly declares corpus rows with no directly supported Relationship and their dependent QASPER cases before either run. |
| Count and order | The retained V1 and V2 corpus rows have the same count and source_doc_id order. |
| Evidence identity | Every retained V1/V2 evidence item has the same ID, content hash, and source metadata. Any evidence difference is a hard failure. |
| Metadata projections | Retained suite, qrels, licenses, transforms, and non-content metadata are unchanged. |
| Relationship-property audit | Every V2 row has a nonempty spanless `relationships` array with valid `evidence_indices`, accepted by the real Go contract validator. |
| Deterministic security gate | The live V2 import passes every row through the same write-time scanner used by production before assessment. |
| Relationship provenance | The manifest records its parent seed ID/hash, retained-evidence identity hash, relationship-ledger hash, and relationship count without recording raw restricted corpus content. The local derivation report records copied-artifact/suite hashes plus fallback source IDs and excluded-ledger count. |

The comparison is not a retrieval-score comparison. It proves that V1 and V2
measure the same retained evidence cohort before measuring live system behavior.

## V2 Seed Generation

The committed cohort lock records the declared exclusions and binds both V1
hashes. The V2 manifest keeps the parent V1 ID/hash, evidence-identity hash,
relationship-ledger hash, and relationship count; its local derivation report
records copied-artifact/suite hashes, fallback source IDs, and the
excluded-ledger count. The executable cohort validator proves that each
retained V1 row and copied sidecar is unchanged before it accepts the V2
derivation. It must not normalize, repair, truncate, or otherwise rewrite
retained evidence, source metadata, answers, suite rows, qrels, licenses, or
transforms.

It adds one or more self-contained Relationship proposals to each retained
corpus item. An evidence item is currently bounded to 999 characters, so a
single fixed proposal is insufficient: `relationships` is an array and every
entry supplies its own inline subject, predicate, object, and
`evidence_indices`. The generator does not emit text ranges or decide canonical
IDs, predicate registration, durable support, or accepted semantic state.

If a historical ledger term is no longer present in its recorded support window
in the frozen V1 evidence, the generator records that source ID and uses a
sentence-bounded best-effort logical proposal instead. This changes only the
non-authoritative proposal; it never changes the evidence, and the live server
 still owns exact grounding, repair, and terminal disposition policy.

Before release use, every row is checked by the same Go code as production:

1. required Relationships use only the current closed fields, cite unique
   in-range `evidence_indices`, and collectively cover the evidence;
2. derivation verifies the selected subject, predicate, and object terms against
   the frozen evidence without serializing their offsets;
3. the source metadata and source revision rules are valid; and
4. the manifest and suite cross-checks pass.

The normal live import then runs the deterministic evidence scanner before the
assessor sees the client relationship proposals.

## Relationship-Property Audit

The generator validates every V2 relationship property with the production Go
contract before release use. Its local derivation report records only parent and
derived seed hashes, the evidence-identity hash, aggregate corpus/relationship/
contract-validation counts, copied-artifact/suite hashes, fallback source IDs,
and excluded-ledger count. It does not write raw evidence, credentials, prompts,
or provider responses to review artifacts.

An incomplete checkpoint, schema mismatch, evidence-identity mismatch, or real
Go contract failure stops seed generation. The live assessor remains the
security and semantic-normalization authority during actual import.

## Live Import and Retrieval Run

A live run uses a dedicated disposable local team against the local Compose
server. It imports through the same public MCP route as normal clients:

~~~text
corpus row
  -> tools/call remember
  -> submission_id
  -> tools/call get_submission_status
  -> completed + current/not_required search state
  -> canonical evidence mapping
  -> tools/call eval_run_recall_case
  -> deterministic qrel scoring
~~~

The runner fails the import gate if a corpus row fails, times out, cannot reach
a terminal state, or cannot produce its canonical mapping. Completed rows must
reach `current` or `not_required` search state. A rejected, quarantined, or
awaiting-review placement is accepted for evidence scoring only when the
terminal response includes a canonical evidence ID; its review burden remains
in the terminal-state counts. The runner does not fake a successful row with a
mock or manual database write.

A quarantined placement is terminal only for retry accounting: the monitor does
not resend it and records it separately. After every row is terminal, the
monitor may retain complete deterministic scores for a V1/V2 diagnostic
comparison, but writes a failed `import_gate_result.json` with
`status: "comparison_only"`. That result is never release-ready, even when the
retrieval-metric `release_gate_result.json` passes.

Any terminal import/placement-monitor failure overwrites
`import_gate_result.json` with `status: "failed"`, `passed: false`, and a
bounded reason. When PostgreSQL counts cannot be read, the count fields are
`null` and `counts_observed` is `false`; a prior passing count must never be
presented as current.

Run outputs contain mapping hashes, run identity, model/configuration
fingerprints, scorer versions, submission terminal-state counts, search-state
counts, recall traces, score summaries, and safe bounded errors. Credentials and
raw provider data remain outside artifacts.

## Baseline, Candidate, and Comparison

Run the pre-change baseline against the validated filtered V1 cohort,
then run the post-change candidate against V2 with the same retained cohort and
suite. The evidence-identity comparison is the apples-to-apples guard; the run
comparison then measures the behavioral effect of adding relationships.

~~~bash
go run ./cmd/eval-runner \
  --mode baseline \
  --seed "$FILTERED_V1_SEED" \
  --suite "$FILTERED_V1_SUITE" \
  --out "$V1_BASELINE_OUT"

go run ./cmd/eval-runner \
  --mode candidate \
  --seed "$V2_SEED" \
  --suite "$V2_SUITE" \
  --out "$V2_CANDIDATE_OUT"

go run ./cmd/eval-runner \
  --mode compare-v2-cohort \
  --baseline-run "$V1_BASELINE_OUT" \
  --candidate-run "$V2_CANDIDATE_OUT" \
  --parent-v1-seed "$PARENT_V1_SEED" \
  --parent-v1-suite "$PARENT_V1_SUITE" \
  --filtered-v1-seed "$FILTERED_V1_SEED" \
  --filtered-v1-suite "$FILTERED_V1_SUITE" \
  --derived-v2-seed "$V2_SEED" \
  --derived-v2-suite "$V2_SUITE" \
  --cohort-lock "$COHORT_LOCK" \
  --out "$V1_V2_COMPARISON_OUT"
~~~

Generic `compare` accepts only two runs with the same seed hash. It must not be
used for this V1-to-V2 comparison.

The comparison reports recall@k, MRR, nDCG, bad-at-k, rank-one rates, and
available context/evidence/dream dimensions. Its interpretation must include
submission acceptance/rejection/quarantine counts and retrieval degradations;
a good score does not excuse unsafe or incomplete ingestion.

`import_gate_result.json` records corpus acceptance separately from the
retrieval-metric `release_gate_result.json`. Both must pass for a release-ready
evaluation. A complete score artifact with `comparison_only` import status may
be used only to diagnose a V1/V2 delta, and the PR must label it non-release-
ready.

## Local E2E Security Gate

Before the 1k evaluation, use the real local Compose server and a live
ephemeral credential to prove the public integration path:

1. tools/list exposes get_submission_status and omits retired placement/review
   tools;
2. a safe spanless remember call with complete evidence-index coverage completes
   and its search state converges;
3. Base64 evidence is rejected before a submission is staged;
4. role-control and prompt-injection evidence are rejected before a submission
   is staged;
5. another profile cannot fetch the first profile’s submission status;
6. recall/trace preserve evidence-first output and flag returned evidence as
   untrusted data; and
7. the migration and repository integration tests prove exact 24-hour raw
   quarantine-payload retention and hard-delete cleanup; immutable source
   evidence and lineage remain restricted audit history.

This is a real PostgreSQL/service test, not a mocked method-call test.

## PR Evaluation Rule

The v2.6 Remember reliability branch is the documented exception above: do not
require a 1k score artifact for it. Record the complete no-skip Compose/UAT
commands and outcomes instead. The normal rule below resumes for subsequent
changes outside that exception.

A pull request that can affect retrieval quality, ranking, submission
assessment, deterministic security scanning, embedding/search documents,
predicate normalization, migration placement, or performance must include:

- passed V2 seed validation;
- passed detailed V1/V2 seed comparison before live evaluation;
- passed Compose-backed MCP security E2E;
- baseline and candidate 1k summaries plus their deterministic comparison;
- terminal submission and search-state counts; and
- any degradation or failure analysis.

A failed mandatory gate blocks the PR. Generated seed files, datasets,
credentials, runtime databases, and full outputs remain ignored. Commit only
the generator/source lock/provenance/validation/scoring code and a compact
safe baseline record.

## Reproducibility Manifest

Every run records:

- seed and suite IDs/hashes;
- for V2, parent V1 ID/hash, retained-evidence identity hash,
  relationship-ledger hash, and relationship count;
- generator and runner versions;
- contract version;
- local server image/revision and relevant provider configuration fingerprints;
- team-scoped mapping hash;
- scorer version and metric configuration; and
- start/end timestamps and safe terminal-state counts.

An identity mismatch is a hard error. The runner must not adopt, overwrite, or
erase a pre-existing runtime with a different identity.

## Evaluation Risks

| Risk | Concrete failure mode | Mitigation |
| --- | --- | --- |
| Unsafe corpus input | Historical data contains encoded or injected text. | Keep retained evidence unchanged and run the real scanner; a deterministic rejection fails import before any assessor call, while only passing rows require a live assessor disposition. |
| Seed semantic drift | Proposal generation changes evidence or cohort while making V2 valid. | Source lock, explicit exclusions, and retained-evidence identity comparison before evaluation. |
| False pass from mock | Import test checks a stub rather than server behavior. | Use public MCP calls against real Compose PostgreSQL/service. |
| Search race | Retrieval starts before required derived documents are current. | Poll submission status and fail the import gate on pending/failed timeout. |
| Cross-team leakage | Eval client reads another profile’s submission or evidence. | Dedicated team/profile tests under RLS and public status boundary. |
| Credential exposure | Evaluation artifacts capture live keys. | Store credential/profile files only in ignored restricted paths; never print or commit them. |
| Score-only decision | Retrieval score hides rejected/quarantined corpus rows. | Publish terminal-state counts and treat ingestion failure as a hard gate. |

## Evaluation Acceptance

1. the V2 seed passes real contract/scanner validation;
2. its detailed V1/V2 comparison passes before any live run;
3. every live corpus row reaches an acceptable terminal submission/search state;
4. baseline, candidate, and comparison artifacts are reproducible from their
   manifests;
5. security E2E uses a real local service and live credential;
6. no raw evidence, provider response, or credential enters committed output;
7. recall metrics and submission outcomes are reported together; and
8. a failing required security, seed, import, or scoring gate cannot produce a
   release-ready result.
