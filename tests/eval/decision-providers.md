# Decision provider validation

Live validation uses the frozen `public_6axis_1k_v3` cohort: 991 documents,
204 cases and seed hash
`0923aa14109291e5d5cf24fe419f0b6fc229823bf3dd4292e17d45447365e675`.
The live access and validation limitations in [ADR 0015](../../adr/0015-optional-typed-decisions.md)
remain in force. Fixture results supply no live quality, cost or latency evidence.

Local verification exercises the real HTTP adapters with protocol fixtures,
application policy, disposable PostgreSQL, production server composition, and
Settings UI. The focused adversarial policy cohort covers temporal qualifiers,
scope, polarity, similar-but-distinct facts, ambiguous equivalence, and suspected
threats. These results prove validation and routing only; fixture answers are not
model judgments and cannot establish zero model false reuse or unsafe acceptance.

When access becomes available, follow `scripts/eval-local.sh` and the existing
local-evaluation documentation using separate baseline/candidate runtimes and
databases, identical seed/source/suite hashes, and private environment files.
Run the baseline with `AI_DECISION_PROVIDER=disabled`; run the candidate with the
approved OpenAI configuration and all four tasks enabled. Never print credentials.
Keep generated imports, run outputs, exchanges, and comparison artifacts in
ignored local paths. Retain source revisions and compact comparison evidence.

Report accuracy/recall quality, false reuse, unsafe passes, fallback frequency,
reported usage or its absence, and p50/p95 stage and complete-operation latency.
Include the focused adversarial cohort in both runs. Require no new unsafe pass
or false reuse in that cohort and no recall-quality regression. Do not weaken
thresholds or alter expected judgments to improve latency. Reranking adds work;
report its quality/latency trade-off separately. A general OpenAI model context
window does not verify an endpoint-specific request ceiling.
