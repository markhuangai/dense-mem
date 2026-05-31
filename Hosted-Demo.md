# Hosted Demo

Use the hosted demo when you want to test Dense-Mem before running Docker:

```text
https://demo-dense-mem.markhuang.ai
```

The demo generates a temporary isolated team and API key. The key lasts 24 hours.
Use only disposable test data.

## Related Tutorials

| Goal | Tutorial |
| --- | --- |
| Try Dense-Mem without installing anything | This hosted demo page |
| Run your own local memory server | [Dense-Mem Quick Start: Give Claude Code and Codex the Same Memory](https://markhuang.ai/blog/dense-mem-personal-server-claude-code-codex) |
| Host Dense-Mem on a public HTTPS server | [Secure Dense-Mem on Vultr with Traefik](https://markhuang.ai/blog/secure-dense-mem-vultr-traefik) |

## What It Proves

Dense-Mem links AI instances by giving them the same external memory service.

![Multiple AI client sessions sharing one temporary Dense-Mem demo key through one hosted memory hub](https://cdn.markhuang.ai/blog/dense-mem-hosted-demo-test-instance/shared-memory-flow.webp)

If two clients use the same generated key, they can read and write the same
temporary memory. If you generate a new key, you create a new isolated team.

This is the core Dense-Mem capability: many AI sessions can use one memory
service, so every linked assistant can work with more useful context.

The AI model still reasons and responds. Dense-Mem stores evidence, claims,
facts, recall results, and clarification tasks so the AI can work with more
context instead of starting cold.

## Limits

| Limit | Demo value |
| --- | --- |
| Session lifetime | 24 hours |
| Authenticated requests | 300 |
| Write attempts | 75 |
| Save attempts | 30 |
| Content stored | 128 KiB |
| Claims | 30 |
| Verifier attempts | 10 |
| Promoted facts | 5 |
| Recall calls | 50 |
| Rate limit | 20 requests per minute |

Do not store secrets, personal data, credentials, production notes, or anything
critical. Expired demo teams and graph data are deleted.

## Step 1: Generate A Key

Open the hosted demo page:

[https://demo-dense-mem.markhuang.ai](https://demo-dense-mem.markhuang.ai)

The page creates a key automatically for the browser tab. You can also click
**Generate New Key**.

Copy the key:

```bash
export DENSE_MEM_API_KEY="dm_..."
```

Use that same key in every client you want to link.

## Step 2: Check MCP Tools

```bash
curl -s "https://demo-dense-mem.markhuang.ai/mcp" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

The response should include high-level tools such as:

```text
remember
recall_memory
reflect_memories
confirm_memory
```

## Step 3: Connect Claude Code

```bash
claude mcp add --transport http dense-mem-demo \
  https://demo-dense-mem.markhuang.ai/mcp \
  --header "Authorization: Bearer $DENSE_MEM_API_KEY"
```

Ask:

```text
Use Dense-Mem to remember that this demo project codename is river-lamp and I prefer concise answers with concrete commands.
```

## Step 4: Connect Codex

Add this to `~/.codex/config.toml`:

```toml
[mcp_servers.dense_mem_demo]
url = "https://demo-dense-mem.markhuang.ai/mcp"
bearer_token_env_var = "DENSE_MEM_API_KEY"
tool_timeout_sec = 60
enabled = true
```

Restart Codex, then ask:

```text
Before answering, use Dense-Mem to recall what you know about my demo project codename and response style.
```

Expected result: Codex should recall the memory written by Claude Code because
both clients use the same demo key.

## Step 5: Try A New Session

Start a new AI session with the same key configured and ask:

```text
What does Dense-Mem remember about this demo?
```

The assistant should recall memory from Dense-Mem instead of depending on the old
chat transcript.

## Step 6: Try A Conflict

Ask:

```text
Remember that I prefer long narrative explanations in this demo.
```

If the client uses the high-level memory workflow, Dense-Mem can return a
clarification instead of silently replacing the earlier concise-answer
preference. The assistant should ask which memory to keep.

## Next

Use the demo to prove the concept. Use the local or Vultr tutorials above when
you want your own server. Use [Quick Start](Quick-Start) and
[Configuration](Configuration) for the manual reference.
