# ADR 0022: Runtime Decision Budgets and Derived Topic Decisions

- Status: Accepted
- Approval: [issue #844](https://github.com/markhuangai/dense-mem/issues/844)
- Refines: ADR 0015
- Supersedes: ADR 0020's Decision uncertainty fallback for derived Topic assignment and Community placement

## Decision

Settings → Decisions stores optional request and per-question input token limits in `app_config`. A missing or cleared override uses the configured `AI_DECISION_*` value. New operations use one settings snapshot. Provider-specific ceilings and the relationship between the two limits are validated before dispatch. A saved override that is invalid for a newly configured provider remains visible and prevents Decision calls until corrected.

Decision requests preserve complete groups and validate the exact serialized request and shared-state-plus-question limits. Batches run under the configured concurrency gate and complete response validation before their answers are used. Maintenance accounts for each batch and regeneration attempt independently. The wire request limit is 64 MiB. Diagnostic capture retains its separate 16 MiB bound and reports omissions.

When enabled, Decision results govern derived Topic assignment. Candidate match probabilities of at least 0.80 select the corresponding Topics. An uncovered probability of at least 0.80 invokes generation for missing subjects. Otherwise an empty selection is uncategorized. Community placement chooses the supplied parent when its probability is at least 0.80 and otherwise leaves the Topic standalone. Structural Topic merge work uses a 0.95 threshold under its own delivery. Provider and budget failures remain visible failures.

Canonical Remember intake retains ADR 0015's 0.99 and 0.01 gates. Disabling the Decision provider retains the existing generative path. The OpenAI model's documented input capacity does not establish the Decisions endpoint's capacity.

## Risk and verification

- A provider switch can invalidate a persisted override. Settings exposes the invalid value and dispatch fails closed until the operator clears or corrects it.
- Concurrent batches can collide in maintenance accounting or hide a required failure. Batch identities are deterministic and distinct, complete attempts reconcile separately, and non-malformed failures prevent regeneration.
- Lower Topic thresholds can change retrieval quality. The maintainer waived the deterministic 1k evaluation for issue #844. Its replacement tests exercise the policy boundaries and real production paths, while empirical quality remains unmeasured.

Verify configuration refresh and rollback, exact vendor wire limits, concurrent dispatch and complete-response validation, real PostgreSQL accounting, public positive and adverse paths, and protected operator diagnostics. Reverting the application restores startup-only budgets and prior Topic routing. Existing `app_config` rows remain available to a subsequent version.
