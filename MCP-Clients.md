# MCP Clients

Dense-Mem exposes MCP Streamable HTTP at:

```text
http://127.0.0.1:8080/mcp
```

Use the hosted demo URL instead when testing the demo:

```text
https://demo-dense-mem.markhuang.ai/mcp
```

## Authentication

Every MCP request uses a Dense-Mem API key:

```text
Authorization: Bearer dm_...
```

For local setup, create a key with:

```bash
docker compose exec server /app/provision-team --name "primary-memory"
export DENSE_MEM_API_KEY="dm_..."
```

## Claude Code

```bash
claude mcp add --transport http dense-mem http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer $DENSE_MEM_API_KEY"
```

For the hosted demo:

```bash
claude mcp add --transport http dense-mem-demo \
  https://demo-dense-mem.markhuang.ai/mcp \
  --header "Authorization: Bearer $DENSE_MEM_API_KEY"
```

## Codex

Add this to `~/.codex/config.toml`:

```toml
[mcp_servers.dense_mem]
url = "http://127.0.0.1:8080/mcp"
bearer_token_env_var = "DENSE_MEM_API_KEY"
tool_timeout_sec = 60
enabled = true
```

For the hosted demo:

```toml
[mcp_servers.dense_mem_demo]
url = "https://demo-dense-mem.markhuang.ai/mcp"
bearer_token_env_var = "DENSE_MEM_API_KEY"
tool_timeout_sec = 60
enabled = true
```

## Check The Tool List

```bash
curl -s "http://127.0.0.1:8080/mcp" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

The response should include tools such as:

```text
remember
recall_memory
trace_memory
assemble_context
reflect_memories
confirm_memory
```

Read-only keys will not see write-scoped tools such as `remember` or
`confirm_memory`.

## Check Bundled Prompts

Dense-Mem also exposes MCP prompts for reusable LLM workflows:

```bash
curl -s "http://127.0.0.1:8080/mcp" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"prompts/list","params":{}}'
```

The response should include `export_memory_as_agent_skill`, a prompt that helps
an LLM recall Dense-Mem context for a topic and draft a shareable Agent Skill
`SKILL.md` file.

## What To Ask

Use normal language:

```text
Remember that I prefer concise explanations with concrete examples.
```

Then ask:

```text
What do you remember about my explanation preferences?
```

The assistant should call Dense-Mem to save and recall memory. If it does not,
check the client MCP configuration, the API key, and the server logs.
