# Runner evaluation

Evaluation campaigns, benchmarks and measurement replay run on GitHub-hosted
runners. Missing runner capacity or Actions configuration blocks execution.
The CLI is workflow-internal and has no local provisioning or resume interface.
Synthetic unit and adapter checks use `bash scripts/test-evaluation.sh`.

Dispatch `production-image-e2e.yml` on the current own-PR branch with `pr_number`
and `evaluation=fixtures`, `live`, or `replay`. Both actors must be repository
admins, and job reruns are rejected. The exact PR head must already have a
successful trusted production-image receipt and full production E2E. The
default dispatch remains the full production E2E route.

The registry generates the matrix. `live` uses all five complete published packs
in [datasets.json](datasets.json), locked to HippoRAG_2 revision
`5ec05b38deecc3318bb432c69865959c56058990`: 45,895 corpus rows and 5,000 questions.
Downloads and runtime data stay in the runner's private temporary directory.
QASPER, custom question subsets, old seed packs and competitor applications are
excluded. The pinned README declares no clear common license; dataset-specific
upstream licensing and attribution remain a reproducibility limitation. The
[source lock](source_locks/hipporag_2.json) records provenance and published joins.

Each dataset imports its entire corpus with ontology disabled into an isolated
ordinary production instance and PostgreSQL database. Terminal receipts verify
every original source and partition. Native reads verify the authenticated
owner, shared space, generation, search contract and canonical fingerprint.
After the first condition, ontology must converge, pause and drain. The second
condition uses unchanged Evidence and provenance. Import, ontology global/team,
and answer concurrency are four; embedding concurrency is two.

Each question gets one initial Recall per condition. Its exact packet is shared
between independent `single` and `multi_turn` conversations. The latter may
Recall, follow returned topics, or Trace explicitly returned Evidence, with eight
memory calls including the initial call. Gold passages and answers are available
only to adapters and deterministic scoring. Complete-response regeneration is
bounded to three attempts per turn; intake has at most two attempts, preserving
the original outcome and supported retry identity. There is no model-based judge.

Generation uses only `DENSE_MEM_EVAL_MODEL`. Ordinary settings resolve in the
calling workflow as workflow value, repository Variable, repository Secret,
then an explicit missing-setting failure. Invalid supplied settings fail.
The workflow pins `DENSE_MEM_EVAL_MODEL` to `dense-mem-testing` and
`DENSE_MEM_EVAL_CONTEXT_WINDOW_TOKENS` to the maintainer-specified `500000`.
`DENSE_MEM_EVAL_VERIFIER_URL` uses the same precedence with an empty workflow pin.
Embedding pins are `@cf/baai/bge-m3`, 1024 dimensions, and the workflow's
Cloudflare endpoint construction using `CLOUDFLARE_ACCOUNT_ID`. If that
construction is absent, `DENSE_MEM_EVAL_EMBED_URL` follows the same fallback.
`DENSE_MEM_EVAL_API_KEY` and `DENSE_MEM_EVAL_EMBED_API_KEY` are dedicated
Secrets-only credentials. Provider URLs and keys cross the reusable workflow as
secret inputs. The maintainer provisions this Actions configuration.

Database passwords and control tokens are generated per dataset job. Resolved
credentials, headers, connection strings and provider URLs never enter console
logs or reports. Existing credential protection and reconstructable payload
chunks retain ordinary exchanges at DEBUG in Actions logs.
The protected chunk records use base64 serialization for exact replay through
Actions log masking.
Initial security rejection stays content-free. There are no additional diagnostic model calls,
encryption dependency or artifact key. Reports contain configured model names
as their provider details.

Initial retrieval R@1/5/10 counts each original canonical source at most once
within the original Evidence slots. Partitions do not create extra source
credit, and undelivered equivalent IDs receive no credit. All-support recovery,
delivered supporting text coverage, answer EM/F1, exact citation validity and
cited gold-source coverage remain separate. Answer normalization follows the
pinned upstream scorer: lowercase, remove ASCII punctuation and English
articles, normalize whitespace, then maximize EM and token-multiset F1 over
published answers and aliases. Failed and missing cases retain full public
denominators. Usage distinguishes available, partial and unavailable attempts;
actual monetary cost remains unavailable when billing cannot be verified.

Dataset jobs stop model work at 330 minutes and end at 360 minutes. The final
30 minutes preserve logs, reports and owned cleanup. `fail-fast: false` and an
always-running aggregate expose failed, timed-out and missing axes. A partial
campaign cannot become a complete baseline. There is no automatic campaign
restart or deadline extension; six-hour feasibility is unproven.

Artifacts retain numeric scores, the comparison matrix, completion counts and
a small reproducibility identity for two days. Raw exchanges remain only in
protected Actions logs. For `evaluation=replay`, also pass `replay_run_id`.
Replay verifies the original run, artifact digest, source/image identity,
complete log hash chain and pinned data before recomputing scores. It starts no
runtime and calls no models. Missing, expired, interrupted or inconsistent logs
produce an explicit unavailable outcome.

[baseline.json](baseline.json) retains the latest overall measured result until
a newer complete campaign and replay are verified. No replacement measurement
is claimed by merely changing this harness. Historical ignored evidence stays
preserved. Guarded historical retirement is documented in
[evaluation-retirement.md](../../architecture/evaluation-retirement.md).

Three temporary test-only bridges remain for trusted `main` CI. They delegate
to the replacement policy tests and have no runtime/provider behavior. A small
follow-up PR removes them after the replacement PR is human-merged.
