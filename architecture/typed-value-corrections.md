# Typed-Value Relationship corrections

Status: design report for [issue #429](https://github.com/markhuangai/dense-mem/issues/429).
This document records the current supported workflow and a bounded recommendation.
It does not change a runtime contract, schema, migration, or accepted ADR.

## Report provenance

- Audit source: the current `main` revision fetched before the report was written.
- Before the report was written, the source checkout and task checkout were
  clean, protected Git configuration was recorded without exposing values, and
  no workflow test artifacts existed. Other registered worktrees and their
  pre-existing state were preserved.
- The behavior observations below describe that audited state. Test results are
  recorded by the implementation workflow after the independent plan audit;
  this report does not claim that unrun checks passed.

## Recommendation

Use the workflow that matches what was wrong in the original observation:

1. For a mis-extracted Entity or predicate, use `correct_relationship` only
   when the complete current effective support set is already known and fits
   the input's 1–200 unique `(evidence_id, start, end)` tuples. Distinct
   occurrences sharing one tuple cannot be represented by this input.
   Public trace/export reads stop at
   100 support rows without pagination, so clients without a retained complete
   set cannot reconstruct a 101–200-support request through those readers.
   Supply the original Relationship version and the entire set; the
   tool atomically supersedes the caller-owned Relationship and records the
   successor's provenance. Relationships with more than 200 effective supports
   have no complete public correction workflow; a truncated set is rejected.
   The correction is also rejected with `inactive_relationship_collision` when
   the corrected identity already exists in the caller's own profile only as
   superseded, unsupported, or other inactive history. A same-team row owned by
   another profile is outside that identity lookup, so the correction creates a
   caller-owned successor without reusing or mutating the other profile's row.
   A caller-owned canonical identity may instead be reactivated by a fresh
   `remember` proposal when its identity, space, and validity match, but that
   path does not preserve the original correction support and provenance.
2. For a fact that has changed in the world, submit a new `remember` request
   with fresh evidence. Do not use `correction_target` to reuse old evidence
   for a new value.
3. For a mis-extracted typed Value, the current public contract has no complete
   atomic replacement workflow. For a single proposal on a
   `current_cardinality = one` predicate, `remember.correction_target` becomes
   stale and rolls back the whole Remember transaction only when the target is a
   matching active sibling under the one-cardinality selector; a semantically
   related target outside that selector can still receive a lineage link. A
   multi-proposal Remember applies observations sequentially, so an earlier
   proposal can supersede and version a target before a later
   `correction_target` check, making that later check stale even when its own
   selector would not match the target. Predicates that allow multiple active
   Relationships likewise can add only a lineage link and leave the older
   Relationship active. `correct_relationship` cannot patch an object Value,
   unit, type, or validity window. Clients must not describe either path as a
   completed typed-Value correction until a follow-up implementation adds that
   capability.

This is the smallest sufficient current guidance. A pair of separate calls
that retracts or replaces one item and then writes another would leave a window
of inconsistent active state and would not preserve one atomic correction
operation.

## Actual workflows

The two candidate workflows have different owners and state transitions:

```text
remember
  MCP registry and schema
    -> Remember application service (authenticated actor, security scan, hash)
    -> synchronous assessment (complete provider response and typed-value checks)
    -> PostgreSQL Remember commit transaction
       evidence, observations, Relationship, support, search documents
       optional correction_target cross-reference

correct_relationship
  MCP registry and schema
    -> lifecycle application service (authenticated owner)
    -> read-only correction and embedding plan
    -> provider embedding execution outside the transaction
    -> PostgreSQL correction transaction
       version/support/space fences, successor, supersession,
       cross-reference, correction event, search documents, receipt
```

The current owners and coverage can be located with these stable symbol and
test anchors (the report intentionally omits file paths and line numbers):

- Registry and contract boundary: `bindRememberTool`, `bindLifecycleTool`,
  `validateCorrectRelationship`, `contractToolDescription`, and
  `ToolCorrectRelationship`.
- Remember assessment and commit: `buildSubmissionAssessmentPlan`,
  `submissionAssessmentCommitInput`, `appendSemanticCorrectionTarget`, and
  `selectRelationshipByIdentity`.
- Lifecycle correction normalization, support projection, and patch resolution:
  `normalizeCorrectRelationshipInput`, `validateCorrectRelationshipInput`,
  `loadEffectiveRelationshipCorrectionSupports`,
  `relationshipCorrectionSupportsEqual`, and
  `resolveRelationshipCorrectionPatch`.
- Trace and export limits: `loadTraceSupports`, `defaultTraceEvents`,
  `maxTraceEvents`, `MaxEvents`, and the memory-pack trace request.
- Real-logic and PostgreSQL coverage: `TestLifecycleCorrectRelationshipUsesAuthenticatedOwner`,
  `TestRelationshipCorrectionReplacesOwnedRelationshipAndPreservesSupport`,
  `TestRelationshipCorrectionRejectsStaleVersionAfterPlan`,
  `TestRelationshipCorrectionPreservesOccurrenceSupport`,
  `TestRelationshipCorrectionEqualHashUsesOneEmbeddingForBothDocumentStates`,
  `TestSemanticOneCardinalitySupersedesPriorActiveRelationship`, and
  `TestSubmissionAssessmentCommitInputRestoresRelationshipTargetOrder`.

The registry binds `remember` and `correct_relationship` to separate
application services. Both services derive
team and owner from the authenticated request context; the request body cannot
select a different tenant.

## Current contracts and behavior

### Remember with `correction_target`

The `remember` input requires evidence and an idempotency key. Relationship
proposals are optional, must cite submitted evidence indices, and accept exactly
one Entity or typed Value object. The Value schema permits the five server-defined
types:
`string`, `number`, `boolean`, `date`, and `date_time`. The transport validator then requires
strings for `date` and `date_time`, a number for `number`, and a boolean for
`boolean`. A Relationship validity window must be ordered.

`correction_target` contains only a target Relationship ID and expected version.
The Remember assessor plan carries it into the semantic observation without
changing the typed Value shape.

On commit, the new assessed Relationship is applied first. When the predicate
has `current_cardinality = one`, applying an active Relationship first
supersedes matching active siblings and increments their versions. If the target
matches that selector (same owner, subject, predicate, polarity, validity start,
scope, active/canonical/support conditions, and predicate-version/cardinality
fences), `appendSemanticCorrectionTarget` then checks the caller-supplied older
version, returns `ErrCorrectionTargetStale`, and the enclosing Remember
transaction rolls back. The target therefore remains active and neither the new
Relationship nor a cross-reference commits.

The commit input keeps `RelationshipObservations` in assessor response order but
restores the separate public `RelationshipResults` slice to proposal order.
The PostgreSQL commit loop applies observations sequentially from that
observation slice, so assessor response order determines which proposal first
versions a target; a caller cannot rely on submitted input order.
If an earlier assessor-response observation supersedes a target under the one-cardinality selector,
that target's version is incremented before a later observation checks its
`correction_target`. A later observation with a different validity start can be
outside the selector yet still fail its expected-version check, causing the
entire request to roll back. This is a deterministic in-batch rollback, not an
independently stale read that should be retried with a refreshed version.

Remember's relationship upsert can reactivate an existing caller-owned canonical
identity. `selectRelationshipByIdentity` finds the row for the same owner,
subject, predicate, object, polarity, and `valid_from`; the upsert requires the
same memory space and `valid_to`, then updates its status to active and attaches
the new evidence.
This is a fresh-evidence state update and can supersede matching one-cardinality
siblings, but it is not the provenance-preserving `correct_relationship` path.

For predicates that allow multiple active Relationships, no sibling
supersession runs. The helper checks the new source version, target version and
owner, the verification event, and a same-predicate semantic relation, then
inserts one `corrects` cross-reference. It does not update the target's status,
version, support, or search document, so an active old Relationship can remain
alongside the new typed Value.

The ordinary one-cardinality supersession test proves the version-advancing
state transition,
but no current test directly exercises a Remember `correction_target` outcome
for either cardinality. That absence is a known coverage gap for this report,
separate from the larger missing typed-Value replacement capability.

The surrounding Remember commit is all-or-nothing in its PostgreSQL transaction,
and provider work occurs before that transaction. A correction-target failure can
therefore reject the complete Remember commit, but a successful correction target
is still a lineage link,
not a supersession operation. Request hashing and attempt replay cover the
whole Remember request.

### `correct_relationship`

The public correction schema accepts `action`, source Relationship ID and
version, support spans, reason, idempotency key, and a patch containing only
`subject_entity`, `predicate`, or `object_entity`. There is no
`object_value`, unit, Value type, or validity-window member. A Value-backed
Relationship cannot be patched to an Entity; the repository rejects that object
kind change.

For a submission, the correction repository requires the source to be caller-owned,
active, canonical, supported, at the expected version, and in a live memory
space. The requested support list must exactly equal the effective support
spans.
Both the schema and repository validator cap that list at 200 entries.
Repeated writes can accumulate more than 200 distinct effective supports on one
Relationship: insertion and counting impose no aggregate cap. Sending all of
them exceeds the input bound; truncating the list fails exact-set comparison.
This is an additional current workflow limit for Entity/predicate corrections.

Support identity has another limit even below those counts. Durable support
identity includes `occurrence_id`, and the correction loader retains every
effective support.
The public support shape contains only `evidence_id`, `start`, and `end`. If two
effective supports share that tuple but differ in occurrence, sending one entry
fails exact-set matching and sending both fails duplicate-span validation. This
supported state has no complete public correction path, even when the caller
knows both occurrences and the trace is complete.

Alias-backed evidence requires an explicit ID conversion without duplicate
spans. `trace_memory` publishes the canonical fragment ID when a support's raw
fragment is present in `evidence_exact_aliases`, while correction support
matching compares the raw `support.fragment_id` loaded from PostgreSQL.
For migrated alias rows, the backfill sets `occurrence_id` to that raw alias
fragment ID, and trace exposes `occurrence_id`.
If the caller independently retained the raw mapping and knows that the row is
a migrated alias, it can use that `occurrence_id` as the correction input's
`evidence_id` with the same span. The public trace shape has no alias marker or
raw `support.fragment_id`, and ordinary occurrence IDs can identify a distinct
occurrence, so a caller cannot safely infer this conversion from the returned
fields alone. Without the independently retained mapping, alias-backed support
has no complete public correction path; using the canonical trace `evidence_id`
alone produces `support_set_mismatch`.

Validity has a separate correction limit. `correct_relationship` copies the
source `valid_from` and `valid_to` values; it has no validity-window patch.
One-cardinality supersession also selects only Relationships with the same
`valid_from`.
Consequently, a mis-extracted validity window has no current atomic correction
path: `correction_target` cannot change it, and a new `remember` proposal with a
different window can leave the original active rather than replacing it. Use
fresh `remember` evidence only when the real-world fact or validity actually
changed later; do not route a validity mis-extraction through that path.

Obtaining the complete set has a separate read limit. `trace_memory` leaves
`MaxEvents` unset, which the PostgreSQL adapter defaults to 100, and
`export_memory_pack` sets it to 100 explicitly. Support reads have no
pagination.
The limit includes historical support rows, so it can hide part of even a
smaller effective set. A 101–200-entry correction remains admissible if the
caller retained the complete current set, but these readers cannot reconstruct
it from scratch. Do not guess missing spans or submit a truncated read as the
complete set. The public request exposes no event-limit or pagination parameter.

Rows below the read ceiling can still overstate the effective set. The trace
support query returns historical support rows without applying the correction
loader's latest support-decision, active-quarantine, evidence-lifecycle, and
current-source-revision filters.
Trace exposes support decisions and lifecycle events but does not expose enough
quarantine or current-source state to reproduce every filter. Sending every
trace row can therefore fail `support_set_mismatch`, while excluding rows from
the public shape cannot be justified reliably. A caller needs a retained
effective set or a future read operation that returns the correction projection.

Entity resolution can return bounded candidates and require one owner
confirmation round.

The correction planner only reads the source, support, projection names, and
active search contract before producing bounded embedding documents. The
lifecycle service executes those embeddings outside the semantic transaction and
passes validated results to the repository. The commit then marks the original
Relationship `superseded`, applies a successor using the existing support
lineage, inserts the `corrects` cross-reference and correction event, updates
search documents, and completes the durable submission in one transaction.

Before that supersession, the commit resolves the corrected identity. An
existing active, supported destination can be reused only when its memory space
and `valid_to` match the source. The identity lookup omits those two fields;
when a caller-owned row matches the identity but either value differs,
successor upsert rejects the mismatch and the transaction rolls back after
planning and provider work. A same-team row owned by another profile is outside
the owner-scoped identity lookup, so the correction creates a caller-owned
successor without reusing or mutating that row. Identity alias rows are
excluded from that lookup, so alias presence alone does not cause a collision;
the canonical row for the identity, when found, determines whether reuse is
possible. An inactive or zero-support canonical destination in the caller's
own identity scope is rejected with
`inactive_relationship_collision`.
Clients must not retry any of these deterministic outcomes as though they were
a transient version conflict.

The correction request hash includes the source ID, expected version, patch,
support spans, and reason. Matching retries reuse the durable submission;
changed requests under the same key conflict.
The public lifecycle port exposes correction planning/commit and evidence
retraction, but not the repository's internal `RetractRelationship` method.

## Current-versus-proposed behavior matrix

| Situation | Current `remember` behavior | Current `correct_relationship` behavior | Supported recommendation now | Proposed bounded extension (future scope only) |
| --- | --- | --- | --- | --- |
| Entity or predicate was extracted incorrectly from the cited evidence | For predicates allowing multiple active Relationships, can store a new assessed Relationship and optionally attach `correction_target`; the target remains in its prior lifecycle state. For `current_cardinality = one`, a matching active sibling follows the stale-version rollback described below, while a semantically related target outside that selector can receive only a lineage link and remain active. | Can replace the caller-owned Relationship, preserve exact effective supports, and atomically supersede the source when the complete set fits the 200-entry input bound and each support has a distinct public tuple. | Use `correct_relationship` with the current source version only when the complete current support set is known and representable. Public reads stop at 100 rows; larger input capacity does not guarantee a complete read-to-correction workflow. | Keep the existing Entity/predicate correction path; no typed-value extension is needed. |
| Corrected Entity/predicate identity already exists only as inactive or unsupported history | A new Remember proposal can reactivate an exact caller-owned canonical identity when its identity, space, and validity match, attaching fresh evidence; it does not mutate another owner's row or an identity alias. A same-team row owned by another profile is not reused or mutated because identity lookup is owner-scoped. | Identity resolution rejects an inactive or zero-support canonical destination in the caller's own identity scope as `inactive_relationship_collision` before source supersession. Identity alias rows are excluded from that lookup and do not themselves trigger the rejection. A matching row owned by another profile is outside that lookup, so the correction creates a caller-owned successor. | Use fresh Remember only for a fresh-evidence state update. For provenance-preserving correction, no public revive path exists for the caller's inactive identity; do not retry that deterministic rejection as a stale-version conflict. | Define explicit destination-collision semantics before extending correction, with history and ownership tests. |
| Typed Value was extracted incorrectly for a `current_cardinality = one` predicate whose target is a matching active sibling | Applying the new active Relationship supersedes the target and increments its version before `correction_target` checks the supplied version; the check returns `ErrCorrectionTargetStale`, so the whole Remember transaction rolls back and no correction link commits. A prior observation in the same request can cause the same stale check for a later proposal even when that later proposal's selector would not match the target. | Cannot patch the object Value or unit; a Value object cannot become an Entity. | No complete public atomic correction exists. Treat the stale result as a rolled-back attempt; do not present `correction_target` as replacement or repeat a deterministic in-batch rollback. | Add a typed-Value patch that resolves or creates the canonical Value, reuses exact supports, and supersedes the source atomically. |
| Typed Value was extracted incorrectly for a `current_cardinality = one` predicate whose target is semantically related but not a matching active sibling | The target is not selected for one-cardinality supersession, so its expected version remains current and `correction_target` can commit only a `corrects` lineage link; the old Relationship remains active. | Cannot patch the object Value or unit; a Value object cannot become an Entity. | No complete public atomic correction exists. Do not present the lineage link as replacement; track a bounded follow-up. | Add a typed-Value patch that resolves or creates the canonical Value, reuses exact supports, and supersedes the source atomically. |
| Numeric Value or unit was extracted incorrectly for a predicate allowing multiple active Relationships | Accepts a typed Value and can attach a correction cross-reference, but does not supersede the old Relationship. | Cannot patch the object Value or unit; a Value object cannot become an Entity. | No complete public atomic correction exists. Do not present `correction_target` as replacement; track a bounded follow-up. | Add a typed-Value patch that resolves or creates the canonical Value, reuses exact supports, and supersedes the source atomically. |
| `date` or `date_time` was extracted incorrectly | Accepts the typed Value as a string and validates the Relationship validity window. For multiple-cardinality predicates, or one-cardinality targets outside the matching-sibling selector, it can add only a lineage link; a matching one-cardinality sibling follows the stale-version rollback described above. | Cannot patch the Value type or canonical value, and preserves the source validity window. | Same typed-Value gap; do not use a two-call approximation. | Apply the same typed-Value replacement rules, with strict date representation and explicit validity evidence. |
| `string` or `boolean` Value was extracted incorrectly | Accepts and validates the typed Value. For multiple-cardinality predicates, or one-cardinality targets outside the matching-sibling selector, it has the same lineage-only `correction_target` behavior; a matching one-cardinality sibling follows the stale-version rollback described above. | Cannot patch any Value object. | Same typed-Value gap. | Replace the canonical typed Value under the same owner, version, support, and atomic supersession fences. |
| Value type changed (for example `number` to `date`) | A fresh proposal can store the new type. A multiple-cardinality `correction_target`, or a one-cardinality target outside the matching-sibling selector, leaves the old Relationship active; a matching one-cardinality sibling follows the stale-version rollback described above. | No Value-type patch exists. | Treat as a new fact or a follow-up correction design; never coerce the old Value in place. | Permit a type change only when the original evidence proves mis-extraction; otherwise reject this path and require fresh Remember evidence. |
| The real-world fact changed after the original evidence | A new Remember submission can carry fresh evidence and a new validity window. Known evidence may supplement, but accepted Relationships retain submitted support. | Reuses the existing effective support set and is therefore not a fresh-fact intake path. | Use `remember` with independent fresh evidence and no correction target. | Preserve this routing and reject typed-Value correction requests that are actually new facts. |
| Same-team owner corrects a Relationship | Authentication fixes the owner; request fields cannot override it. | Source mutation requires the caller to own the source; same-team visibility is not mutation authority. | Reject wrong-owner attempts with the existing bounded error. | Reuse the same authenticated owner check for Value patches and every created or reused Value. |
| Source version is stale | `correction_target.expected_version` is checked during the semantic commit; a mismatch rejects the Remember transaction. An earlier observation in the same request can also increment a later target before its check. | Submit and confirm fence the source version; pending confirmation becomes a bounded changed-state rejection. | Refresh and retry with a new key when an independent version change caused staleness. Matching-sibling or earlier-in-batch correction-target rollback leaves the target unchanged, so do not repeat that request: use eligible Entity/predicate correction or the documented no-current-workflow outcome for typed Values. | Require the source expected version for Value patches and return the existing typed stale result without partial state. |
| Support is missing, altered, or from the wrong space | Accepted Relationships require support; known evidence is bounded and must not replace submitted support. | Support spans must exactly match effective supports, contain at most 200 entries, and remain in the Relationship's memory space; truncation is rejected. | Preserve exact evidence IDs, occurrences, spans, source revisions, and space ownership. Trace/export reads return at most 100 support rows without pagination; do not infer the complete effective set from a truncated result. | Reuse exact effective support only for a proven mis-extraction; keep support and space checks identical. |
| Two effective supports share an evidence ID and span but differ in occurrence ID | Can preserve both occurrence-specific supports on one Relationship. | The input cannot distinguish them: one tuple fails full-set matching and two identical tuples fail validation. | No complete public correction path exists for this state; do not collapse distinct occurrence provenance. | Decide how correction identifies and preserves each occurrence, then prove the colliding-tuple case with real PostgreSQL and production-entry tests. |
| A support is stored under an exact-evidence alias | Can retain the alias-backed support and its canonical occurrence provenance. | Trace publishes the canonical fragment ID, while correction matching expects the raw alias fragment ID. For migrated alias rows, trace `occurrence_id` carries that raw ID, but the public shape has no alias marker; only a caller that retained the raw mapping can safely submit it as `evidence_id`. The canonical ID alone yields `support_set_mismatch`. | Without an independently retained mapping, alias-backed support has no complete public correction path. | Keep the conversion explicit and prove alias-backed and ordinary occurrence cases with real PostgreSQL and production-entry tests. |
| Trace returns historical or otherwise ineffective support rows below the read ceiling | Can preserve support decisions and lifecycle history, but those rows are not necessarily the current correction-effective set. | Correction filters latest support decisions, active quarantines, evidence lifecycle, and source revisions before exact-set matching; trace returns the raw support rows and does not expose enough quarantine/current-source state to reproduce all filters. | No complete public read-to-correction path exists from trace alone, even below 100 rows; retain the effective set or use a future effective-support reader. | Expose or derive one authoritative effective-support projection before extending correction input. |
| Validity was mis-extracted from the cited evidence | Can propose a different ordered validity window, but this does not atomically replace the existing Relationship. | Correction copies the source validity window; no validity patch is exposed, and one-cardinality supersession requires the same `valid_from`. | No complete current atomic correction exists. Do not route a mis-extracted window to fresh Remember or infer a replacement through `correction_target`. | Require a validity patch whose evidence explicitly supports the corrected window, with the same owner, version, support, and atomic supersession fences. |
| Retry or replay | Whole-request hash and durable attempt state replay the authoritative result or return an idempotency conflict. | Correction hash and durable submission replay the authoritative receipt or reject a changed request under the same key. | Keep retry keys scoped to the authenticated team/profile and include every correction field in the hash. | Include the complete typed-Value patch, support set, validity, and reason in the existing correction hash. |
| Provenance and atomicity | Evidence, observation, verification, support, search state, and optional cross-reference commit together. | Original/successor states, copied supports, cross-reference, correction event, search state, and receipt commit together. | Never claim a successful lineage link is an atomic replacement. | Keep provider work outside the transaction and atomically commit Value resolution, source supersession, successor, lineage, history, and search state. |

The trade-offs are explicit:

| Decision dimension | Retain and document Remember | Bounded `correct_relationship` extension (future) |
| --- | --- | --- |
| Compatibility | Zero runtime or wire changes, but the typed-Value replacement gap remains visible to clients. | Add an optional typed-Value patch while retaining existing Entity/predicate inputs and outputs. |
| Client effort | Existing Remember callers can submit evidence, but cannot obtain one atomic replacement for a typed Value. | Clients need the source version and complete effective supports before submitting the typed patch. The current 100-row trace/export limit constrains that workflow and must be accounted for in a separately approved extension. |
| Policy ownership | Remember assessment and semantic commit remain the owners; `correction_target` stays a lineage operation. | Lifecycle correction remains the single owner of replacement and reuses the existing version, support, ownership, and search fences. |
| Implementation and test cost | Documentation only; no new persistence, provider, migration, or E2E work. | Requires Value resolution, request hashing, provider/search fencing, rollback proof, and real PostgreSQL plus production-entry positive/adverse tests. |

The recommendation is to retain Remember for fresh facts, retain the existing
`correct_relationship` path for Entity/predicate mistakes, and schedule a
separate approved implementation for the typed-Value extension. The extension
column describes a target behavior only; it is not an API change in this
report.

## Mis-extraction versus changed fact

Consider an evidence span that says “the package weighs 12 kg.” If the assessor
stored `120 kg`, the error is a mis-extraction: the cited span is still the
source of truth. A future typed-Value correction must be able to reuse that
exact occurrence, source revision, quote, authority, and span while replacing
the Value-backed Relationship atomically.

If a later observation says “the package now weighs 15 kg,” that is a changed
fact. It needs a new Remember evidence item and its own assessment, support,
validity, and possible conflict handling. The old `12 kg` evidence cannot be
silently repurposed to support `15 kg`. The same distinction applies to dates,
date-times, units, strings, booleans, and a change of Value type.

## Bounded future implementation scope

The comparison establishes a real gap but does not authorize its implementation
in this report. A later approved issue should keep the extension inside the
existing lifecycle owner and correction transaction:

- Add a mutually exclusive typed-Value patch variant to the correction input,
  covering Value type, canonical value, optional unit/display, and the existing
  normalization policy. Do not coerce a supplied Value into another type or
  mutate a `value_records` row in place; resolve or create the canonical Value
  and point the successor Relationship at it.
- Decide validity-window patch semantics explicitly. A correction that changes
  validity must reuse evidence that states the corrected window; an actual
  later fact or validity change is routed through fresh Remember evidence. It
  must never infer a new time window from an old span that does not contain it.
- Retain the current owner, expected-version, active/canonical/support-count,
  exact-support, memory-space, collision, confirmation, request-hash, provider
  fence, and search-document invariants. Keep provider execution outside the
  authoritative transaction and commit source supersession, successor state,
  lineage, history, and search state atomically.
- Preserve occurrence IDs, source IDs and revisions, quotes, authority,
  support decisions, and correction event metadata. A changed fact must not be
  admitted through this support-reuse path merely because its Value has the
  same type or unit. Decide whether correction input carries occurrence identity
  or uses another deterministic preservation mechanism when distinct supports
  share a public evidence/span tuple. Preserve the alias-to-raw-fragment
  conversion for migrated exact-evidence aliases and do not apply it to
  ordinary occurrence IDs without confirming the mapping.
- Add real PostgreSQL positive and adverse cases for each Value type and unit,
  wrong owner, stale version, support mismatch/revision change, validity
  change, replay conflict, ambiguous selection, active collision, search-fence
  failure, and rollback. Include two effective supports with the same canonical
  evidence and span but different occurrence IDs. Add a production-entry
  positive/adverse scenario when the public contract changes, including that
  occurrence collision.

No new field, migration, registry description, public API, or ADR is proposed
for acceptance by issue #429 itself.

## Evidence and known limits

The current contract and policy are covered by real-logic and PostgreSQL tests,
including typed Value and validity validation, Remember correction-target
propagation and complete-commit input, typed Value canonical de-duplication,
correction replacement, owner isolation, replay, support preservation, active
collision, atomic search behavior, stale-version and support-revision fences,
occurrence provenance, exact-evidence alias canonicalization, known-evidence
ownership and support isolation, and public correction success and adverse
provider, stale-state, and ownership-isolation scenarios. The occurrence and
alias suites each record the remaining gap: no direct correction case covers
two distinct occurrences with one public tuple or conversion of a public
canonical alias ID to its retained raw ID.

These tests prove the existing Entity/predicate correction path and typed Value
intake. They do not prove atomic replacement of a Value, because the current
`correct_relationship` schema has no Value patch and no test can exercise one.
That missing proof is the implementation gap recorded here, not a reason to
expand this design-only change.

## Scope and rollback

This report changes no runtime behavior, canonical data, public schema, or
accepted architecture. Reverting the one document removes the recommendation;
there is no data rollback or migration boundary. Any future implementation
requires a separately approved issue, updated contract, real PostgreSQL and
production-entry tests, and an independent plan audit.
