# Entity Identity And Resolution

An Entity has an opaque durable ID. Its display name, aliases, and kind describe
it; a string match alone does not establish real-world identity. All resolution
is bounded by the authenticated team and memory space.

## Core Rule

Reuse an existing Entity when the supplied evidence and allowed context support
that identity. Create an Entity only when accepted evidence establishes a new
identity. Do not guess through genuine ambiguity.

A subject is an Entity. A Relationship object is exactly one Entity or typed
Value. Scalar Values have canonical type/value/unit semantics and should not
be turned into Entity names merely to fit a graph shape.

## Names And Aliases

Canonical and alias names are metadata on an Entity. They are not edges.
A normalized name is an indexed lookup value, not a unique Entity ID:
different people or systems can share a name.

Candidate queries remain scoped before IDs leave storage. An identity in
shared memory and a similarly named identity in private memory cannot be
merged by cross-space lookup.

## Submitted Identity Hints

A Remember Entity hint supplies a name or a server-issued `known_entity_id`.
An exact ID constrains selection and is revalidated for scope/currentness.
An optional kind is a non-authoritative hint. Callers must not fabricate IDs.

Without a known ID, the server builds bounded exact canonical/alias candidate
sets and occurrence groundings from the proposal's cited evidence. Candidate
overflow is a visible failure; silently hiding one exact match would make
ambiguity look like certainty.

Known evidence is explicit read-only context. It does not become a new
observation or receive submitted-evidence security results.

## Grounding And Assessment

The server finds token-bounded name/alias occurrences and issues opaque
boundary references. The assessor chooses within those references.
Pronouns can use only the supplied server-issued anchor for an earlier exact
name span; an unsupported pronoun does not authorize an invented identity.

The assessor returns a complete batch of normalized Entity and Relationship
results. Server validation checks coverage, uniqueness, allowed candidates,
kind compatibility, cited ranges, and exact constraints. Repairs regenerate
the complete response within the same bounded conversation.

Unsupported proposals receive `not_stored`; safe evidence can still be
stored. New facts and Relationships are not discovered during this assessment.

## Identity At Commit

Accepted proposals reference canonical endpoints selected through server
policy. The transaction rechecks current identity and source/space fences.
Within a request and across concurrent requests, name text is not a global
deduplication key and cannot erase valid homonyms.

A repeated accepted Relationship attaches to the applicable owner/space
identity. Another owner's equivalent Relationship remains separately authored.
See [Relationship Model And Lifecycle](Relationship-Model-And-Lifecycle).

## Merge And Split Corrections

There is no standalone public MCP Entity merge/split operation.
`correct_relationship` can correct the subject or Entity object of a
caller-owned active Relationship while preserving its support lineage.
It changes that authored Relationship, not every row that references an Entity.

A supplied exact Entity ID must be active and visible. A name-based correction
can return `awaiting_confirmation` with bounded candidates. The owner selects
only returned candidates using the supplied token; the original Relationship
stays active until the accepted correction commits.

When several observations wrongly combine two people, inspect and correct the
owned Relationships individually. Do not infer that one correction globally
splits an Entity or changes another profile's authored state. Fresh evidence
belongs in Remember.

Typed-Value correction limits are described in
[Conflicts And Corrections](Conflicts-And-Corrections).

## Security And Failure

Identity queries use authenticated team and space context. Inaccessible IDs do
not reveal whether another team's Entity exists. Provider output cannot
replace authenticated ownership or select out-of-allowlist durable IDs.

Required provider, lookup, validation, or persistence failure stays typed.
An empty candidate set is not a substitute for a failed lookup. No partial
Entity/Relationship commit may survive an invalid complete response.

## Risks And Verification

| Failure mode | Mitigation |
| --- | --- |
| Two different people with the same name become one Entity | Preserve homonyms and require evidence-supported resolution. |
| A nickname becomes a new edge | Keep aliases as Entity metadata. |
| An exact hint crosses a private or team boundary | Revalidate ID scope at lookup and commit. |
| A correction changes another author's knowledge | Restrict replacement to the authenticated owner's Relationship. |
| A pronoun is resolved without an anchor | Accept only server-issued grounding and complete-response validation. |

Verify named/alias reuse, homonyms, known IDs, pronouns, within-batch identity,
and concurrent writers through actual assessor validation and PostgreSQL tests.
