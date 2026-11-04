# Typed-Value Relationship corrections

The supported correction workflow depends on the original observation and its
current effective support. Accepted ADRs govern any future contract change.

## Choosing a correction workflow

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
test anchors:

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
proposals are required, must collectively cite every submitted evidence index, and accept exactly
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

## Mis-extraction versus changed fact

If evidence says “the package weighs 12 kg” but the stored Value is `120 kg`,
the cited occurrence remains the source of truth. Correcting that extraction
must preserve its source revision, quote, authority and exact span. The current
public correction contract has no atomic typed-Value replacement operation.

If later evidence says “the package now weighs 15 kg”, submit fresh evidence
through Remember with its own assessment and validity. The old `12 kg` evidence
cannot support `15 kg`. The same distinction applies to dates, date-times,
units, strings, booleans and Value types.
