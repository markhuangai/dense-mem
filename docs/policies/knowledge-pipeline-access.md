# Policy: Knowledge Pipeline Access Control

## Scope

This policy governs who can read, write, verify, promote, and retract
knowledge-pipeline entities (Fragments, Claims, Facts) within dense-mem.

## Principals

| Principal | Authentication | Capabilities |
|-----------|---------------|--------------|
| Team profile (standard key, write) | Team-profile bearer key | Create, delete, retract fragments; create, delete, verify, promote claims |
| Team profile (standard key, read) | Same | Read and list fragments, claims, facts; recall |
| Operator command | Local/container command with datastore access | Team provisioning, profile key rotation/revocation, maintenance workflows outside the public HTTP surface |
| Unauthenticated | None | None — deny by default |

## Deny-by-Default

All endpoints require a valid API key. There are no public endpoints in the
knowledge pipeline. The OpenAPI spec endpoint (`/api/v1/openapi.json`) requires
authentication.

## Cross-Team Access

A standard API key MUST only be used for the team it was issued for. Requests
where the key's team does not match the requested team are rejected with 403.

Operator maintenance runs outside the public HTTP surface. When those workflows
emit audit records, they use the system actor context.

## Principle of Least Privilege

Handlers check the required scope (`read` or `write`) before processing. Scopes
are attached to the team profile at creation time and cannot be escalated.

The `recall` endpoint requires `read` scope only. Recall never mutates state.
