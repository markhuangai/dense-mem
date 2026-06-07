# Dense-Mem MCP Proxy

`dense-mem-mcp-proxy` exposes a Dense-Mem Streamable HTTP MCP server as a local
stdio MCP process. Use it for MCP clients that can run local stdio commands but
do not reliably load Streamable HTTP MCP servers.

Install/run with `npx`:

```bash
npx -y dense-mem-mcp-proxy
```

Use environment variables for normal desktop-client configuration:

```bash
DENSE_MEM_MCP_URL=http://127.0.0.1:8080/mcp \
DENSE_MEM_API_KEY=dm_live_... \
npx -y dense-mem-mcp-proxy
```

You can also pass headers with an environment variable:

```bash
DENSE_MEM_MCP_URL=http://127.0.0.1:8080/mcp \
DENSE_MEM_MCP_HEADERS='{"Authorization":"Bearer dm_live_..."}' \
npx -y dense-mem-mcp-proxy
```

Arguments are still supported when env vars are not available:

```bash
npx -y dense-mem-mcp-proxy \
  --url http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer dm_live_..."
```

Extra headers can be passed with repeated `--header "Name: value"` flags or with
`DENSE_MEM_MCP_HEADERS` as a JSON object.

The proxy writes MCP JSON-RPC frames to stdout. Diagnostic logs go to stderr and
redact Authorization headers and Dense-Mem API keys.

## Codex Desktop

Use a team-specific config key when one client connects to multiple Dense-Mem
teams. Dense-Mem also returns the authenticated team name and description through
MCP discovery, but many clients display or namespace tools by this local key.

```toml
[mcp_servers.dense_mem_primary_memory]
command = "npx"
args = ["-y", "dense-mem-mcp-proxy"]
env = { DENSE_MEM_MCP_URL = "http://127.0.0.1:8080/mcp", DENSE_MEM_API_KEY = "dm_live_..." }
tool_timeout_sec = 60
enabled = true
```

## Claude Desktop

```json
{
  "mcpServers": {
    "dense_mem_primary_memory": {
      "command": "npx",
      "args": ["-y", "dense-mem-mcp-proxy"],
      "env": {
        "DENSE_MEM_MCP_URL": "http://127.0.0.1:8080/mcp",
        "DENSE_MEM_API_KEY": "dm_live_..."
      }
    }
  }
}
```

## Local Checkout

For development before publishing or while testing local changes, run the proxy
directly from the repository:

```bash
DENSE_MEM_MCP_URL=http://127.0.0.1:8080/mcp \
DENSE_MEM_API_KEY=dm_live_... \
node /path/to/dense-mem/packages/mcp-proxy/bin/dense-mem-mcp-proxy.js
```
