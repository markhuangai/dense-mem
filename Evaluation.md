# Evaluation

Dense-Mem evaluates retrieval through the production MCP submission and recall
paths. The evaluation corpus, downloaded datasets, runtime databases,
credentials, and full run outputs are local-only ignored artifacts. The
generator, source locks, validation, scoring, and compact baseline evidence are
reviewed with code.

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

The historical V1 seed preserves corpus identity but cannot enter the new
submission API because it lacks required exact-span proposals. The V2 seed is a
source-locked, auditable derivative of that V1 corpus.

~~~text
V1 source-locked corpus
  -> deterministic safe-content normalization
  -> exact-span proposal generation
  -> model-only security/conformance audit
  -> real Go scanner + proposal validation
  -> detailed V1/V2 comparison
  -> live submission import and retrieval evaluation
~~~

## V1-to-V2 Seed Comparison Gate

No live evaluation begins until the generated V2 seed has passed both
validation and a detailed V1/V2 comparison.

The comparison reports, without exposing raw restricted corpus content:

| Check | Required result |
| --- | --- |
| Source lock | V1 manifest/hash matches the approved source. |
| Count and order | Same corpus row count and source_doc_id order. |
| Metadata projections | Suite, qrels, licenses, transforms, and non-content metadata are unchanged. |
| Content normalization | Every changed row has a documented safe deterministic operation and before/after hash. |
| Proposal audit | Every V2 row has a complete exact-span proposal accepted by the real Go contract validator. |
| Security audit | Every V2 row has a passed model-only security/conformance decision. |
| Regeneration | Regeneration warnings/failures are zero or explicitly fail the gate. |

The comparison is not a retrieval-score comparison. It establishes that the V2
corpus is a usable semantic equivalent of V1 before measuring live system
behavior.

## V2 Seed Generation

The V2 generator keeps a parent source hash, generator version, normalization
report, and proposal audit report in its manifest. It may make only bounded
deterministic content repairs needed for the current security contract, such as
removing hidden controls or a known terminal control directive from a
LongMemEval chat wrapper. It must not silently rewrite answer, suite, qrel,
license, or transform projections.

It produces a smallest meaningful lexical Entity/Relationship proposal for each
corpus item. The generator does not decide canonical IDs or accepted semantic
state.

Before release use, every row is checked by the same Go code as production:

1. the deterministic evidence scanner must pass;
2. the required proposal must satisfy exact Unicode code-point span rules;
3. the source metadata and source revision rules must be valid; and
4. the manifest and suite cross-checks must pass.

## Model-Only Seed Audit

The generator runs a separate model-only security/conformance audit for all V2
rows. Its narrow job is to detect prompt-injection/security concerns in the
proposed evidence package; it does not normalize semantic meaning or choose
server state.

The audit uses strict structured output and records only row ID, hashes, policy
version, verdict, bounded category, and retry count in its local checkpoint.
It does not write raw evidence, credentials, prompts, or provider responses to
review artifacts.

A non-passing verdict, warning, incomplete checkpoint, transport retry that
does not recover, or schema mismatch fails seed generation. An unaudited
deterministic-only output is for local development and is not a release seed.

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
  -> tools/call recall_memory
  -> deterministic qrel scoring
~~~

The runner fails the import gate if a corpus row is rejected, quarantined, fails,
times out, cannot reach a current/not_required required search state, or cannot
produce its canonical mapping. It does not fake a successful row with a mock or
manual database write.

Run outputs contain mapping hashes, run identity, model/configuration
fingerprints, scorer versions, submission terminal-state counts, search-state
counts, recall traces, score summaries, and safe bounded errors. Credentials and
raw provider data remain outside artifacts.

## Baseline, Candidate, and Comparison

Run a baseline and candidate against the same validated V2 seed, same suite,
and isolated runtime identity. Then compare their generated summaries:

~~~bash
go run ./cmd/eval-runner --mode baseline   --seed "$SEED" --suite "$SUITE" --out "$BASELINE_OUT"

go run ./cmd/eval-runner --mode candidate   --seed "$SEED" --suite "$SUITE" --out "$CANDIDATE_OUT"

go run ./cmd/eval-runner --mode compare   --baseline-run "$BASELINE_OUT"   --candidate-run "$CANDIDATE_OUT"   --out "$COMPARISON_OUT"
~~~

The comparison reports recall@k, MRR, nDCG, bad-at-k, rank-one rates, and
available context/evidence/dream dimensions. Its interpretation must include
submission acceptance/rejection/quarantine counts and retrieval degradations;
a good score does not excuse unsafe or incomplete ingestion.

## Local E2E Security Gate

Before the 1k evaluation, use the real local Compose server and a live
ephemeral credential to prove the public integration path:

1. tools/list exposes get_submission_status and omits retired placement/review
   tools;
2. a safe span-grounded remember call completes and its search state converges;
3. Base64 evidence is rejected before a submission is staged;
4. prompt-injection evidence is rejected before a submission is staged;
5. another profile cannot fetch the first profile’s submission status;
6. recall/trace preserve evidence-first output and flag returned evidence as
   untrusted data; and
7. the migration and repository integration tests prove exact 24-hour
   quarantine retention and hard-delete cleanup.

This is a real PostgreSQL/service test, not a mocked method-call test.

## PR Evaluation Rule

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
- parent V1 source hash and V2 normalization/audit policy hashes;
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
| Unsafe corpus input | Historical data contains encoded or injected text. | Normalize only documented safe cases, run real scanner and model-only audit, fail closed. |
| Seed semantic drift | Proposal generator changes content/qrels while making V2 valid. | Source lock plus detailed projection/hash comparison before evaluation. |
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

