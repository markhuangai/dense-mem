# Using Dense-Mem

After setup, Dense-Mem works in the background. Your AI assistant calls it when
memory should be saved, searched, reviewed, or corrected.

## The Normal Flow

```mermaid
sequenceDiagram
  participant You
  participant Assistant
  participant DM as Dense-Mem

  You->>Assistant: Remember my review style.
  Assistant->>DM: remember(...)
  DM-->>Assistant: saved fact or clarification
  You->>Assistant: What should you know before editing this repo?
  Assistant->>DM: recall_memory(...)
  DM-->>Assistant: relevant memories
  Assistant-->>You: answer using recalled context
```

## What To Ask Your Assistant

Use normal language. You do not need to know the tool names.

| What you want | Example prompt |
| --- | --- |
| Save a preference | "Remember that I prefer direct answers and no filler." |
| Save a project fact | "Remember that this repo deploys through Docker Swarm." |
| Recall context | "Before we start, recall what you know about this project." |
| Check memory health | "Review my stored memories and tell me if anything needs clarification." |
| Correct a memory | "Replace the old deployment note with this new one." |
| Share reusable knowledge | "Package the validated React testing memories so another team can inspect and import them." |

## What Dense-Mem Returns

Dense-Mem can return several kinds of memory:

| Kind | Meaning |
| --- | --- |
| Fact | A promoted memory that passed checks and is active. |
| Claim | A candidate memory that has evidence but may not be promoted. |
| Fragment | The original text used as evidence. |
| Clarification | A question the assistant should ask before changing memory. |

For normal use, facts are the most important. Claims and fragments help explain
where a memory came from.

## Example: Personal Preference

You say:

```text
Remember that I prefer code reviews to start with bugs and risks before style notes.
```

Later you ask:

```text
Review this pull request using my usual review style.
```

The assistant can recall the preference and structure the review around bugs and
risks first.

## Example: Project Setup

You say:

```text
Remember that the Dense-Mem local control portal should stay bound to 127.0.0.1.
```

Later you ask:

```text
Help me update the Docker setup.
```

The assistant can recall that the portal should stay local and avoid suggesting
an unsafe public bind.

## Example: Conflicting Memory

You say:

```text
Remember that I want long detailed answers.
```

If Dense-Mem already has an active fact that you prefer concise answers, it
should not silently choose one. It can return a clarification so the assistant
asks:

```text
Which preference should I keep: concise answers or long detailed answers?
```

After you answer, the assistant can confirm which memory should stay active.

## Portable Skill Packs

Skill packs move selected memory between Dense-Mem teams or deployments. They are
best for reusable knowledge such as project onboarding facts, coding standards,
tooling conventions, or domain notes that another assistant should inspect
before trusting.

The normal workflow is:

```mermaid
sequenceDiagram
  participant Source as Source team
  participant Assistant
  participant Target as Target team

  Assistant->>Source: find_skill_pack_candidates(query)
  Source-->>Assistant: candidate facts and validated claims
  Assistant->>Source: export_skill_pack(...)
  Source-->>Assistant: canonical_json and sha256
  Assistant->>Target: inspect_skill_pack(...)
  Target-->>Assistant: duplicates, conflicts, and decisions
  Assistant->>Target: import_skill_pack(...)
  Target-->>Assistant: import_id and item results
```

Ask your assistant to do this in normal language:

```text
Find memories about our repository review standards that could be exported as a
skill pack.
```

Then:

```text
Export the selected memories as a skill pack named "Repo review standards" and
show me the SHA-256 hash.
```

Before importing into another team, inspect the artifact:

```text
Inspect this skill pack JSON before importing it. Tell me which items are new,
duplicates, or conflicts.
```

Import in review mode when you want the pack to create evidence and candidate
claims without promoting trusted facts:

```text
Import this skill pack in review mode.
```

Use trusted mode only for artifacts you are willing to treat as authoritative.
Trusted mode can validate imported claims and can promote `source_fact` items to
active facts. If an item conflicts with an active local fact, Dense-Mem returns
`decisions_required` instead of silently replacing local memory.

Conflict decisions are explicit:

| Decision | Meaning |
| --- | --- |
| `skip` | Do not import that item. |
| `import_anyway` | Import the item without superseding local facts. |
| `supersede_local` | Supersede conflicting active local facts with the imported item. |
| `demote_to_claim` | For `source_fact` items, keep the import as a validated claim instead of promoting it to an active fact. |

Every applied import returns an `import_id`. Keep it if you may need rollback:

```text
Rollback skill-pack import 11111111-2222-3333-4444-555555555555.
```

Rollback uses the import ledger. It blocks if an imported graph entity changed
after import, so you can review the conflict instead of undoing newer work.

## Teams And Profiles

Dense-Mem keys are tied to a team and a named profile.

Use these terms this way:

| Term | Meaning |
| --- | --- |
| Team | Top-level memory boundary. Use separate teams when memory should not mix. |
| Profile | Named key identity inside a team, usually one assistant, role, workflow, or automation. |
| Read-write key | Can remember, import, confirm, and otherwise mutate memory for its team/profile. |
| Read-only key | Can recall, search, and inspect memory, but cannot write or change it. |

Use this when you want separate memory spaces:

| Situation | Suggested setup |
| --- | --- |
| One person, one assistant | One team, one profile |
| One person, multiple roles | One team, one profile per role |
| Multiple projects | One team per project |
| Automation that should only read | Read-only profile key |

Do not share a write key with tools that should only search memory. See
[Read-Only Keys](Technical-Reference#read-only-keys) for the API example.

## Daily Operator Commands

List teams:

```bash
docker compose exec server /app/list-teams
```

List profiles in a team:

```bash
docker compose exec server /app/list-team-profiles --team-id "<team-id>"
```

Rotate a profile key:

```bash
docker compose exec server /app/rotate-team-profile-key \
  --team-id "<team-id>" \
  --profile-id "<profile-id>"
```

## When To Use The Control Portal

Use the local control portal to:

- create teams
- create named profiles
- create read-only or read-write API keys
- rotate keys
- remove keys
- review or update IP ban settings

The portal is not a memory browser. It does not show every fact, claim, or graph
node.
