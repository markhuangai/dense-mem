# Relationship Model And Lifecycle

Entity and typed Value are semantic nodes. A Relationship records one author's
supported connection between those endpoints. PostgreSQL owns its state and
append-only provenance; a SemanticEdge is an eligible read projection.

## Canonical Vocabulary

| Record | Responsibility |
| --- | --- |
| Entity / Entity name | Real-world identity plus canonical/alias display metadata. |
| Value | Canonical typed scalar: string, number, boolean, date, or date-time. |
| Predicate | Registered meaning, allowed endpoint kinds, state/event behavior, and current cardinality. |
| Evidence fragment / occurrence | Canonical source item and exact submitted occurrence with provenance. |
| Relationship observation | What submitted evidence expressed, including exact grounding. |
| Verification event / semantic assessment | Validated assessment and its provenance; nullable verifier fields remain nullable. |
| Relationship record | Owner-scoped semantic identity, episode, version, and current lifecycle state. |
| Evidence support / support decision | Exact cited support and append-only grant/revoke/reinstate history. |
| Cross-reference / transition | Authored lineage links and lifecycle/validity changes. |
| Conflict / Hypothesis | Separate workflow or speculation; neither is an active semantic edge. |

A subject is an Entity. An object is exactly one Entity or typed Value.
Names are not durable IDs; aliases are not Relationships.

## Profile Ownership And Team Visibility

The authenticated permanent owner alias supplies `owner_profile_id`.
Ordinary support, retraction, correction, and supersession operations require
ownership. A same-team reader cannot mutate another author's record.

Visibility additionally depends on the memory space. Shared records are
team-visible; private records require the bound identity/credential context.
Server IDs, support, and cross-references must remain within that boundary.

A profile can submit its own independently supported position about a shared
subject. This preserves both authors and can create a governed conflict.
System conflict review has a distinct bounded authority to close losing
episodes; it does not transfer authorship.

## Relationship Identity

A canonical Relationship distinguishes owner, endpoints, registered predicate,
polarity, applicable scope, and temporal episode. Repeated support from the
same owner can attach to the existing identity. Equivalent meaning across
owners is grouped for recall without merging ownership or lifecycle.

`semantic_group_key` supports that read grouping. A group is not a new truth
record, a source of evidence, or permission to alter every member.

## Predicate Behavior

Predicate definitions contain a key and aliases, allowed subject/object kinds,
`relationship_kind` (`state` or `event`), `current_cardinality`
(`one` or `many`), lifecycle state, and version.

A state describes a continuing condition; an event describes an occurrence.
Single-current cardinality constrains comparable current values. It does not
mean the latest unverified claim automatically wins.

The client proposes a key or supplies an exact active known key. The assessor
selects allowed context or requests registration. The server rechecks and
registers compatible definitions inside the accepted transaction with
provenance. Provider output cannot directly create policy.

## Evidence And Effective Support

Support cites exact evidence/occurrence spans using Unicode code-point offsets,
start inclusive and end exclusive. It retains provenance, source authority,
and owner context.

The latest support decision determines whether a link is granted/reinstated or
revoked. Effectiveness also depends on evidence lifecycle, current source
revision, allowed space/generation, Relationship lifecycle, and requested time.
A stored support row alone is not proof of current support.

Known evidence supplied to Remember is read-only assessor context. Each stored
proposal split must still cite submitted evidence. Recalled text, graph
context, communities, and Hypotheses cannot be resubmitted as independent proof.

## Status And Effective Support

The domain represents `active`, `pending_evidence`, `needs_review`,
`rejected`, `retracted`, `superseded`, `quarantined`, and `disputed`
Relationship states. These are Relationship lifecycle values, not the public
Remember processing states.

An active graph edge and default recall require active, eligible effective
support. A source can lose effectiveness after retraction, supersession,
source advancement, private erasure, or a governed conflict decision.

Current Remember stores accepted splits or reports unsupported proposals as
`not_stored`. It does not require every safe evidence item to create a
Relationship. See [Memory Write Pipeline](Write-Pipeline).

## Temporal State

`valid_at` asks what held in the real world; `known_at` asks what the system
knew at a time. Current-state pointers and append-only events serve different
purposes. A later correction or source retirement does not rewrite the earlier
system-knowledge view.

Submitted validity bounds must be grounded and validated. Scope, cardinality,
polarity, and time are checked together when determining comparable episodes.

## Correction And Retraction

`correct_relationship` replaces a caller-owned active Relationship using
its current version and complete effective support set. It atomically
supersedes the original, creates or reuses a supported successor, and appends
correction lineage.

The supported patch changes the subject Entity, predicate, or Entity object.
It cannot replace a typed Value or validity window. A fresh factual change
requires new evidence through Remember.

`retract_evidence` withdraws owned evidence and recomputes affected support
and lifecycle while retaining provenance. Physical private erasure is a
separate acknowledged portal operation.

Full workflows and limitations are in
[Conflicts And Corrections](Conflicts-And-Corrections).

## Trace And SemanticEdge Reads

Trace returns authorized observation/support/decision/transition lineage and
bounded graph/conflict context. Verifier verdict and confidence fields that
are absent remain null; callers must not fabricate a verdict or score.

Trace is bounded. A non-null `stopped_reason` signals incomplete context.
Support rows include history: derive a correction support set from latest
decisions, not by copying every row. Public trace/export support limits can
prevent reconstruction of a large complete set.

A SemanticEdge contains the current eligible Relationship's endpoints and
predicate. Candidate, review-held, and Hypothesis records do not enter that
read merely because their endpoints exist.

## Atomicity And Failure

Accepted evidence, semantic changes, support/lifecycle history, conflict effects,
search documents, required vectors, and the terminal write result commit
together. Provider calls occur outside that transaction. A failed required
operation cannot leave a partial accepted semantic subset.

Derived search state cannot overwrite a newer source version or become a second
authority. PostgreSQL ownership and RLS constraints remain part of the proof.

## Risks And Verification

| Failure mode | Protection |
| --- | --- |
| Raw support rows are counted after revocation | Latest decisions plus source/lifecycle/space eligibility. |
| Equivalent profiles are merged into one author | Group only in read projections; retain authored rows. |
| A single-current predicate silently overwrites a disputed position | Apply evidence, temporal, and conflict policy before supersession. |
| A nullable assessment is treated as an entailed verdict | Preserve null in trace and contracts. |
| A correction guesses missing support history | Require the complete effective set and respect trace bounds. |
