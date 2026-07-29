# MCP Clients

Dense-Mem exposes MCP Streamable HTTP:

```text
local:  http://127.0.0.1:8080/mcp
demo:   https://demo-dense-mem.markhuang.ai/mcp
```

Every protected request uses:

```text
Authorization: Bearer <Dense-Mem API key>
```

## Claude Code

The installed Claude CLI exposes the HTTP transport/header form:

```bash
claude mcp add --transport http dense-mem \
  http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer $DENSE_MEM_API_KEY"
```

For the demo:

```bash
claude mcp add --transport http dense-mem-demo \
  https://demo-dense-mem.markhuang.ai/mcp \
  --header "Authorization: Bearer $DENSE_MEM_API_KEY"
```

Run `claude mcp add --help` on the installed version before scripting the
command across machines.

## Codex

Codex uses `config.toml` for Streamable HTTP MCP servers. The supported fields
include `url`, `bearer_token_env_var`, `tool_timeout_sec`, and `enabled`.

```toml
[mcp_servers.dense_mem]
url = "http://127.0.0.1:8080/mcp"
bearer_token_env_var = "DENSE_MEM_API_KEY"
tool_timeout_sec = 60
enabled = true
```

For the demo:

```toml
[mcp_servers.dense_mem_demo]
url = "https://demo-dense-mem.markhuang.ai/mcp"
bearer_token_env_var = "DENSE_MEM_API_KEY"
tool_timeout_sec = 60
enabled = true
```

User configuration normally lives at `~/.codex/config.toml`; a trusted project
can use `.codex/config.toml`. Restart/refresh the Codex host after changing MCP
configuration, then use `codex mcp list` or `/mcp`.

The version-sensitive source is the
[official Codex MCP documentation](https://developers.openai.com/codex/mcp).

## Tool Discovery

After connection, the LLM host initializes the MCP server and calls
`tools/list`. Use the host's MCP status/tool view to confirm the visible
catalog; ordinary memory use should not build a separate HTTP integration.

Compare with [MCP Tool Catalog](Technical-Reference#mcp-tool-catalog).
Visibility depends on scope, role, feature flags, and contract version.

`tools/list` only describes available names, purposes, and input schemas. The
host uses `tools/call` when the model selects a tool. Their canonical behavior
is in
[Request Lifecycle And Security](Request-Lifecycle-And-Security#mcp-memory-interface).

For contract `dense-mem.v2.3.2`, verify:

- `remember` takes evidence and optional proposal
- `get_memory_placement` returns processing/search state, bounded assessment
  policy, and semantic `review_tasks[]`
- `remember` accepts per-item `supersedes_evidence_ids` for caller-owned current
  evidence; this direct lifecycle path cannot be combined with
  `previous_source_revision`
- `retract_evidence` requires caller-owned evidence IDs, a bounded reason, and
  an idempotency key; it appends a lifecycle decision instead of deleting data
- each semantic review exposes only stable ID/version, kind, state, question,
  server-supplied options, bounded guidance, and `expires_at`
- `resolve_memory_placement` may select a supplied option or submit exact new
  evidence; it never accepts a client-supplied confidence score
- `recall_memory` returns ranked Evidence contexts plus separate
  `related_relationships[]`, `related_communities[]`, `conflicts[]`,
  `related_hypotheses[]`, `search_states`, and `degradations[]`
- `trace_memory` takes `relationship_id`
- retired lifecycle fields are absent

## Server Instructions

The MCP initialization response should use the server `instructions`
field for short cross-tool guidance:

```text
Remember accepts evidence asynchronously. Poll get_memory_placement.
To replace exact authored evidence, use supersedes_evidence_ids on the new
evidence item. To withdraw it without a replacement, call retract_evidence.
Both actions preserve append-only provenance and do not permit cross-profile
targets.
For a review, select only a server-supplied option when exact current-session
context resolves it. Otherwise ask the user the returned question. Selecting an
Entity or predicate option makes no AI call; accepting or correcting support
requires exact new evidence and creates one new assessed placement item.
Default recall returns bounded Evidence contexts plus compact Relationship
discovery results in related_relationships. Trace relationship_id for
evidence/history. Pending Relationships and Hypotheses are not active
knowledge. Never guess identity review.
```

Tool descriptions still own tool-specific input/output rules. Server
instructions do not override client/user safety policy.

## Prompts

Use the LLM host's MCP prompt browser to inspect bundled prompts advertised by
the connected server.

A bundled export prompt may help a client turn recalled context into a
standalone Agent Skill. Generated files are not automatically imported into
Dense-Mem.

## First Smoke

Ask the client to:

1. remember one small statement
2. poll until processing completes
3. when review is returned, inspect its expiry and either select an exact
   supplied option or ask the user; never infer an unavailable choice
4. inspect search state without treating vector freshness as write truth
5. recall the statement
6. trace its `relationship_id`

Use [Quick Start](Quick-Start#6-remember-evidence) and
[Using Dense-Mem](Using-Dense-Mem).

## Troubleshooting

| Symptom | Check |
| --- | --- |
| server absent | URL, network, TLS, client restart |
| unauthorized | environment variable exported to client process, key expiry/revocation |
| tool missing | scopes, feature flag, contract version |
| old request rejected | client/server contract mismatch |
| recall misses recent write | processing outcome, branch degradation, and search freshness |
| timeout | client tool timeout versus provider/placement polling pattern |

Authentication and transport security are in
[Request Lifecycle And Security](Request-Lifecycle-And-Security).
