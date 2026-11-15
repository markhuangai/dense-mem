# Runner evaluation

Evaluation campaigns and benchmarks run on GitHub-hosted runners. Missing
runner capacity or Actions configuration blocks execution.
The CLI is workflow-internal and has no local provisioning or resume interface.
Synthetic unit and adapter checks use `bash scripts/test-evaluation.sh`.

To evaluate a published image without a PR, open **Actions → Run evaluation →
Run workflow**, select `main`, and enter a Dense-Mem production image tag or
digest. The default `mode=live` runs the complete benchmark. For example:

```bash
gh workflow run run-evaluation.yml --repo markhuangai/dense-mem --ref main \
  -f image=ghcr.io/markhuangai/dense-mem:YOUR_TAG
```

The image must come from `ghcr.io/markhuangai/dense-mem` and carry Dense-Mem
production source metadata. A tag is resolved once to its immutable digest before
any provider credentials reach dataset jobs. The run summary records the digest
and image source revision; numeric reports record that digest and the exact runner
source revision. Initiating and triggering actors must be repository admins;
dispatches from other branches and job reruns are rejected.

Use `mode=fixtures` after changing evaluation workflows, adapters, capture,
scoring, or cleanup. It imports three small documents and asks two questions
against a real production instance and PostgreSQL, using a deterministic local
provider inside the runner. Both ontology conditions and both answer policies
produce eight scored answers. It checks the evaluation plumbing without calling
real model APIs; its scores do not measure retrieval or model quality.
The fixture mixes rows with and without published indices to check source identity
and gold mappings against both source formats.

PR validation retains its existing entrypoint: dispatch `production-image-e2e.yml`
on the current own-PR branch with `pr_number` and `evaluation=fixtures` or `live`.
That route requires the exact PR head's successful trusted production-image
receipt and full production E2E. Its `evaluation=none` default
runs ordinary production E2E. The manual image route has no PR or preview-receipt
requirement.

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
every original source and partition. Rows without a published `idx` use their
zero-based position as `dataset:row:N`, while integer indices retain `dataset:N`.
Native reads verify the authenticated owner, shared space, generation, search
contract and canonical fingerprint.
After the first condition, ontology must converge, pause and drain. The second
condition uses unchanged Evidence and provenance. Import, ontology global/team,
and answer concurrency are four; embedding concurrency is two.
The isolated instance pins its MCP request limit to 200000 per minute, above
the complete pack's bounded call count, so the shared production default cannot
throttle the campaign.

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
Embedding pins are OpenAI's `text-embedding-3-small`, 1536 dimensions, and
`https://api.openai.com/v1`. The workflow URL pin takes precedence over
repository `DENSE_MEM_EVAL_EMBED_URL` Variables and Secrets.
`DENSE_MEM_EVAL_API_KEY` and `DENSE_MEM_EVAL_EMBED_API_KEY` are dedicated
Secrets-only credentials; the latter must contain the OpenAI embedding API key.
Provider URLs and keys cross the reusable workflow as
secret inputs. The maintainer provisions this Actions configuration.

Database passwords and control tokens are generated per dataset job. Resolved
credentials, headers, connection strings and provider URLs never enter console
logs or reports. Existing credential protection and reconstructable payload
chunks retain ordinary exchanges at DEBUG in Actions logs.
The protected chunk records use base64 serialization to preserve exact protected
payloads through Actions log masking.
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
Per-axis `host_elapsed_ms` sums recorded host execution time, including failed
hosts. `successful_answers_per_host_second` divides completed answers by that
sum and is unavailable when no host timing was recorded. Mean host latency uses
observed hosts. Each host starts after its shared initial Recall is available;
that Recall's latency remains in the captured packet. Workers can overlap, and
whole-campaign `elapsed_ms` separately includes import, ontology and log capture.

Dataset jobs stop model work at 330 minutes and end at 360 minutes. The final
30 minutes preserve logs, reports and owned cleanup. `fail-fast: false` and an
always-running aggregate expose failed, timed-out and missing axes. A partial
campaign cannot become a complete baseline. There is no automatic campaign
restart or deadline extension; six-hour feasibility is unproven.

Artifacts retain numeric scores, the comparison matrix, completion counts and
a small reproducibility identity for two days. Raw exchanges remain only in
protected Actions logs.

Native ontology PostgreSQL measurement regressions retain their fixture protocols
and runner instructions in [their module guide](../../internal/ontology/postgres/README.md).

[baseline.json](baseline.json) retains the latest overall measured result until
a newer complete campaign is verified. No replacement measurement
is claimed by merely changing this harness. Historical ignored evidence stays
preserved. Guarded historical retirement is documented in
[evaluation-retirement.md](../../architecture/evaluation-retirement.md).

Three temporary test-only bridges remain for trusted `main` CI. They delegate
to the replacement policy tests and have no runtime/provider behavior. A small
follow-up PR removes them after the replacement PR is human-merged.
