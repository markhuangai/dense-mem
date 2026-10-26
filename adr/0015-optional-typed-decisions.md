# ADR 0015: Optional Typed Decision Providers

- Status: Accepted
- Date: 2026-10-26
- Supersedes: None
- Refines: ADR 0001, ADR 0002, ADR 0003, ADR 0004, ADR 0009, ADR 0011
- Maintainer approval: directly approved decision-provider implementation plan

## Decision

One optional typed decision port serves OpenAI Decisions and TypeSafe Jev.
Its predicates, choices, and scores preserve probability distributions, vendor
confidence, refusals, resolved model identity, usage, and bounded diagnostics
separately. Provider adapters translate HTTP protocols and validate complete
responses. Existing application owners retain authorization, thresholds, semantic
validation, lifecycle policy, and atomic PostgreSQL commit authority.

Deployment config uses independent `AI_DECISION_*` settings through the existing
configuration loader. Unset or explicitly disabled preserves existing behavior;
enabled providers require complete configuration and never inherit credentials.
Configured request ceilings are distinct from documented vendor capacity.
Jev defaults to 64k/request and 32k/shared-state-plus-question ceilings; explicit
long-context opt-in allows 262k for both with vendor latency/accuracy trade-offs.
The OpenAI model's 1,050,000 total/922,000 maximum input window does not verify the
Decisions endpoint ceiling. OpenAI permits at most 400 questions per HTTP request.

All four supported tasks default on when a provider is configured and settings
have been read successfully. Settings → Decisions persists supplied per-task
preferences in existing PostgreSQL `app_config`; explicit false survives startup
and provider changes. Effective state is `provider_configured AND preference`,
where missing preference means true. The existing five-second refresh supplies
new operation snapshots. A failed refresh retains an explicitly stale last-valid
snapshot; no valid snapshot is a visible failure for required work. A running
operation keeps its settings revision and authorized candidate snapshot.

Evidence deduplication reuses exactly one authorized equivalent with probability
≥0.99 when every alternative is ≤0.01. All ≤0.01 means new evidence. Security
retains deterministic screening and passes only a complete set of hazard
probabilities ≤0.01. Conflict selection needs both position and support
probabilities ≥0.99; its explanation is an identified server-policy reason.
Other valid answers route the complete uncertain task to generation. Required
transport, authentication, refusal, malformed-response, and generative failures
remain failures. Invalid responses trigger bounded complete regeneration with no
answer splicing or partial semantic commit. Vendor confidence never replaces a
probability gate or the existing generative confidence policy.

Recall scores authorized, hydrated shortlists of at most 50 evidence and 20
Relationships per space after database reads, with a fixed relevance rubric,
stable original-order score ties, existing temporal/grouping/fusion rules, and
final requested limits. Optional failures retain original ranking with an
explicit degradation. Cancellation fails. Final provenance and feedback describe
the selected results; community ranking remains unchanged.

Persist task provenance in existing assessment/event JSON: input hash, provider
and model identity, settings revision/source, policy revision, thresholds,
answers, and routing reason. Public MCP inputs and durable history stay unchanged.
No new settings table, worker, durable workflow, probe endpoint, or diagnostic
model call is introduced. Detailed operator logs remain available independently
of telemetry and protect credentials in supported serialized forms. Initial
security rejection remains content-free; admitted diagnostics are bounded and
report capture/truncation status.

## Risk

- A confident false equivalence or unsafe classification could alter memory;
  retain conservative gates, complete validation, authorized allowlists, and
  atomic commits, and verify adversarial scope/time/polarity cases.
- Settings defaults could overwrite an explicit off or a failure could restore
  defaults; persist only supplied preferences and distinguish absence, stale
  snapshots, and unavailable initial state.
- Ranking can change selected results or add latency; preserve deterministic
  filtering/fusion and final-selection provenance, measure full operations, and
  report optional degradation explicitly.
- Detailed logs can expose credentials or overflow useful bounds; register
  decision secrets with existing protection and retain bounded capture metadata.

## Validation and limitations

Require independent plan conformance before executing tests, real disposable
PostgreSQL persistence/isolation/replay/fence/rollback coverage, both HTTP vendor
fixtures, Settings UI checks at desktop/tablet/mobile widths, registered
production-entry scenarios, and the repository's review/full-validation gates.

The maintainer subsequently directed “I don't have it right now and let's skip
it for now” when asked for private OpenAI configuration. This defers the live
OpenAI comparison on frozen `public_6axis_1k_v3` (991 documents, 204 cases) and its
credential prerequisite. Fixtures and adversarial policy regressions replace no
live quality or speed evidence. OpenAI and Jev live accuracy, false reuse, unsafe
passes, fallback frequency, usage, and p50/p95 latency remain pending access.
Do not weaken gates or change judgments for a speed result.

References: [OpenAI Decisions](https://developers.openai.com/api/docs/guides/decisions),
[GPT-6 Luna](https://developers.openai.com/api/docs/models/gpt-6-luna),
[TypeSafe models](https://docs.typesafe.ai/models), and
[TypeSafe API](https://docs.typesafe.ai/api/system-one).
