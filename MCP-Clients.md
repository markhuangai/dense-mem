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

For target v2, verify:

- `remember` takes evidence and optional proposal
- `get_memory_placement` returns processing and search state
- `recall_memory` returns ranked evidence contexts plus compact relationship discovery paths
- `trace_memory` takes `relationship_id`
- retired lifecycle fields are absent

## Server Instructions

The target MCP initialization response should use the server `instructions`
field for short cross-tool guidance:

```text
Remember accepts evidence asynchronously. Poll get_memory_placement.
Default recall returns bounded evidence contexts plus compact relationship
discovery paths. Trace relationship_id for evidence/history. Pending
candidates and Hypotheses are not active knowledge. Never guess identity
review.
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
3. inspect search state without treating vector freshness as write truth
4. recall the statement
5. trace its `relationship_id`

Use [Quick Start](Quick-Start#6-remember-evidence) and
[Using Dense-Mem](Using-Dense-Mem).

## Troubleshooting

| Symptom | Check |
| --- | --- |
| server absent | URL, network, TLS, client restart |
| unauthorized | environment variable exported to client process, key expiry/revocation |
| tool missing | scopes, feature flag, contract version |
| old request rejected | v1/v2 schema mismatch |
| recall misses recent write | processing outcome, branch degradation, and search freshness |
| timeout | client tool timeout versus provider/placement polling pattern |

Authentication and transport security are in
[Request Lifecycle And Security](Request-Lifecycle-And-Security).
