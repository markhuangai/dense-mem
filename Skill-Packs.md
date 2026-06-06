# Skill Packs

Skill packs package selected Dense-Mem knowledge into portable JSON. Use them
for reusable skills such as workflows, project conventions, or operational
patterns that should move between teams or profiles.

For exact tool schemas and artifact fields, see
[Technical Reference](Technical-Reference#skill-pack-tools).

## When To Use A Skill Pack

| Use case | Example |
| --- | --- |
| Reuse a workflow | Export a blog-writing workflow learned from project history. |
| Share operational knowledge | Move a deployment checklist from one profile to another. |
| Preserve support evidence | Export the facts plus supporting claims and fragments. |
| Review an external pack | Inspect a pack before importing anything. |

Do not use skill packs for secrets, credentials, or personal data that should not
move between profiles.

## Workflow

| Step | Tool | Writes memory? |
| --- | --- | --- |
| Find candidates | `find_skill_pack_candidates` | No |
| Export selected items | `export_skill_pack` | No |
| Inspect a pack | `inspect_skill_pack` | No |
| Import a pack | `import_skill_pack` | Yes |
| Roll back an import | `rollback_skill_pack_import` | Yes |

`inspect_skill_pack` is the no-write review path. `import_skill_pack` creates an
import ledger entry and can create or update claims and facts depending on mode,
selected items, and conflict decisions.

## Find Candidates

```json
{
  "query": "blog writing workflow",
  "limit": 10
}
```

Candidates come from existing active facts and validated claims with supported
skill-pack predicates.

## Export A Pack

```json
{
  "name": "blog-writing",
  "description": "Reusable blog-writing workflow",
  "fact_ids": ["fact_123"],
  "include_support": true
}
```

The export result includes:

| Field | Meaning |
| --- | --- |
| `canonical_json` | The portable JSON artifact. |
| `sha256` | Integrity hash for the canonical artifact. |
| `filename` | Suggested `.skill-pack.json` filename. |

Keep `include_support=true` unless you intentionally need to omit source
evidence.

## Inspect Before Importing

```json
{
  "artifact_json": "{...}",
  "expected_sha256": "..."
}
```

Inspection reports duplicates, conflicts, superseded matches, matching claims,
and any decisions required before a trusted import can proceed.

For a remote artifact, use an HTTPS URL:

```json
{
  "url": "https://example.com/blog-writing.skill-pack.json",
  "expected_sha256": "..."
}
```

## Import Modes

| Mode | Behavior |
| --- | --- |
| `review` | Imports selected items as reviewable claims. It does not promote source facts to active facts. |
| `trusted` | Treats the pack as trusted source material and can validate claims, promote source facts, or supersede local facts when decisions allow it. |

Trusted URL imports require `expected_sha256`.

Example review import:

```json
{
  "artifact_json": "{...}",
  "mode": "review"
}
```

Example trusted import with a conflict decision:

```json
{
  "artifact_json": "{...}",
  "expected_sha256": "...",
  "mode": "trusted",
  "conflict_decisions": [
    { "index": 0, "action": "demote_to_claim" }
  ]
}
```

Supported conflict actions are:

| Action | Meaning |
| --- | --- |
| `import_anyway` | Import despite the local match or conflict. |
| `skip` | Do not import that item. |
| `supersede_local` | Replace conflicting local active facts. |
| `demote_to_claim` | Keep source-fact material as a claim instead of an active fact. |

## Roll Back An Import

```json
{
  "import_id": "import_123"
}
```

Rollback uses the import ledger. It aborts when affected graph entities changed
after import, because reverting those changes could erase unrelated later work.
