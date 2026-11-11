# MCP Clients

Connect an MCP Streamable HTTP host to the team-scoped URL copied from `/ui` → **Connect**:

```text
https://memory.example.com/teams/<team-id>/mcp
```

For local use, the main listener is `http://127.0.0.1:8080`. `/mcp` also supports authenticated unscoped access where team resolution permits it. Exact identity, token, and cookie rules belong to [Request Lifecycle And Security](Request-Lifecycle-And-Security).

## Credentials And Discovery

```mermaid
%%{init: {"sequence": {"rightAngles": true}}}%%
sequenceDiagram
  participant Host as MCP host
  participant Server as Dense-Mem
  Host->>Server: Initialize over authenticated Streamable HTTP
  Server-->>Host: Negotiated protocol and instructions
  Host->>Server: tools/list
  Server-->>Host: Visible current schemas
  Host->>Server: tools/call using discovered schema
  Server-->>Host: Terminal result or actionable error
```

Supply an API key or configured customer-IdP OAuth access token in the bearer header. Browser SSO/portal cookies do not authenticate MCP. Configure credentials privately and allow the server's bounded synchronous operation time.

OAuth-aware hosts can use the RFC 9728 resource metadata and `401` challenge link. Those require configured `MCP_PUBLIC_BASE_URL`; the portal's explicitly labeled browser-origin fallback is only a manual connection aid. See [OAuth authentication](Request-Lifecycle-And-Security#oauth-bearer-authentication-for-mcp).

## Claude Code

The installed Claude CLI exposes the HTTP transport/header form:

~~~bash
claude mcp add --transport http dense-mem   "http://127.0.0.1:8080/teams/$DENSE_MEM_TEAM_ID/mcp"   --header "Authorization: Bearer $DENSE_MEM_API_KEY"
~~~

Set `DENSE_MEM_TEAM_ID` to the full UUID shown in `/ui`. Run
`claude mcp add --help` on the installed version before scripting the command
across machines.

## Codex

Codex uses config.toml for Streamable HTTP MCP servers:

~~~toml
[mcp_servers.dense_mem]
url = "http://127.0.0.1:8080/teams/00000000-0000-0000-0000-000000000000/mcp"
bearer_token_env_var = "DENSE_MEM_API_KEY"
tool_timeout_sec = 240
enabled = true
~~~

Replace the zero UUID and base URL with the value copied from `/ui`. User
configuration normally lives at ~/.codex/config.toml; a trusted project can
use .codex/config.toml. Restart or refresh the host after changing
configuration, then use `codex mcp list` or `/mcp`.

Make `DENSE_MEM_API_KEY` available to the Codex process without committing it to
a configuration file. The 240-second client timeout allows the server's
180-second Remember budget plus transport overhead; it does not extend server
processing limits.

The version-sensitive source is the
[official Codex MCP documentation](https://developers.openai.com/codex/mcp).

## Verify The Connection

| Check | Expected behavior |
| --- | --- |
| Initialize and discover | Host negotiates protocol and reads the authenticated catalog. |
| Remember schema | Exact Evidence, explicit destination, retained key; retired graph fields absent. |
| Mutation outcome | Complete terminal receipt or typed failure, without required polling. |
| Recall | Eligible Evidence, current topic hints, conflicts, and explicit degradations. |
| Trace | Authorized `evidence_id`. |

Refresh tool discovery after upgrading; the MCP protocol revision and Dense-Mem contract version are different concepts. Feature/scopes can hide tools, and invocation rechecks the same gates.

## Session And Python Integrations

Use [Framework Integrations](Framework-Integrations) for the typed Python core, Google ADK memory service, and LangChain retriever. Exact private session intake is optional: send stable user-event IDs, original text/timestamps, and a retained complete request. External session/user identifiers never select authorization.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| 401/403 | Correct bearer source, selected team, current credential/membership, and permitted scopes. |
| Missing tool | Current discovery, feature/provider configuration, and effective access. |
| Invalid parameters | Refresh schemas; remove retired graph fields and old trace handles. |
| Timeout or lost response | Preserve request/key and follow [unchanged-request recovery](Write-Pipeline#replay-and-recovery). |
| Browser works, MCP fails | Configure a bearer credential; browser cookies are deliberately separate. |

Host CLI/config syntax can change independently of Dense-Mem. Verify the installed host's help when automating configuration.
