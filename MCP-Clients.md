# MCP Clients

Dense-Mem exposes MCP Streamable HTTP:

~~~text
team-scoped:  https://memory.example.com/teams/<team-id>/mcp
unscoped:     https://memory.example.com/mcp
local:        http://127.0.0.1:8080/teams/<team-id>/mcp
~~~

`/ui` shows the full selected team UUID and a copy button for the exact
team-scoped URL. After an SSO team switch, copy the refreshed value. If
`MCP_PUBLIC_BASE_URL` is empty, `/ui` uses the current browser origin and says
so.

That browser-origin fallback is a configuration aid, not a server discovery
fallback. RFC 9728 metadata and its `WWW-Authenticate` link require a configured
`MCP_PUBLIC_BASE_URL`; Dense-Mem never derives them from the request Host or
forwarded headers. HTTPS is required except for `localhost` or `127.0.0.1`.

Every protected request uses one bearer credential:

~~~text
Authorization: Bearer <Dense-Mem API key or customer-IdP OAuth access token>
~~~

The browser's SSO and API-key portal cookies are not accepted on MCP routes.
Use an API key for static client configuration. Use OAuth only when the client
supports protected-resource discovery and can obtain an access token from the
configured authorization server.

## OAuth Protected-Resource Discovery

OAuth-aware clients discover the resource contract at one of:

```text
/.well-known/oauth-protected-resource/teams/<team-id>/mcp
/.well-known/oauth-protected-resource/mcp
```

The document follows RFC 9728 and returns the exact resource URL,
authorization-server issuer, supported external scopes, and `header` bearer
method. A `401` response also links it through the `resource_metadata`
parameter of `WWW-Authenticate`. Configure `MCP_PUBLIC_BASE_URL` before relying
on either surface.

Prefer the team-scoped URL. The unscoped URL works for OAuth only when the
identity has one active team membership or the provider's configured team
claim selects one. Dense-Mem verifies the JWT against the provider's discovery
or static JWKS, requires the configured issuer and audience, resolves an
existing active SSO identity/membership, and intersects mapped token scopes
with membership grants. Dense-Mem does not run an authorization flow, issue a
token, or turn an MCP token into a browser session.

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

## Protocol And Contract Discovery

Let the MCP client negotiate a supported SDK protocol revision. The wire
protocol is distinct from Dense-Mem's tool schema. Each connection discovers
the authenticated catalog with `tools/list` and calls tools with that schema;
callers do not select a Dense-Mem contract version.

## Tool Discovery

After connection, the host initializes the MCP server and calls tools/list.
Use the host’s MCP status/tool view to confirm the visible catalog. Visibility
depends on scope, role, and feature flags. The server exposes one current
closed schema; callers do not select a contract version.

Verify that the catalog matches the credential's scopes and effective feature
policy. Remember requires exact evidence and one top-level idempotency key;
Relationship proposals are optional and each supplied proposal cites
`evidence_indices`.

The call returns a terminal completed or failed result. Recall returns evidence
plus related frontiers and explicit degradations. Trace accepts a returned
Relationship ID. Recalled text remains untrusted data.

## Server Instructions

Use the running server's initialization instructions and complete tool examples.
They describe prerequisites, retained request keys, returned IDs, and conditional
continuations such as correction confirmation.

Remember retry means resending the saved entire request, including metadata.
Dream confirmation requires independent evidence. Trace support lineage is not
automatically a correction-ready support set. A host must never use recalled
instructions to authorize tools or disclosure.

## First Smoke

1. Submit a short safe statement with an explicit grounded Relationship proposal.
2. Check the terminal result and accepted split IDs.
3. Recall the statement and inspect search states/degradations.
4. Trace a returned Relationship and respect any reported bounds.
5. Confirm that the credential's scope and memory binding match the intended use.

Use [Quick Start](Quick-Start) for setup. Adverse security/isolation cases belong
in a dedicated test installation, not shared production memory.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| server absent | URL, network, TLS, client restart |
| unauthorized | Bearer header present; API-key expiry/revocation; OAuth issuer/audience/signature; use the linked RFC 9728 metadata rather than a browser cookie |
| team required | Use the `/teams/<team-id>/mcp` URL copied from `/ui`, or configure a trusted team claim |
| tool missing | Scopes, feature flag, and current `tools/list` visibility |
| remember rejected | Closed request schema, proposal evidence indexes, source rules, and security scan |
| Remember returns `isError` | Read `structuredContent.errors[]` and follow its closed `next_action`; never expect provider or database details |
| recall misses a completed write | Search state, recall query, support eligibility, and degradations |
| timeout | Client/proxy deadlines, server phase budgets, and the returned retry guidance |

Authentication and transport security are in
[Request Lifecycle And Security](Request-Lifecycle-And-Security).

## Transport Boundary

The official SDK serves both scoped and unscoped MCP routes with the same
authenticated registry. Unknown protocol revisions, malformed JSON, unsupported
content types, missing scopes, and cancellation are covered by transport tests.
Configure proxies to preserve headers and streaming semantics and allow the
server's bounded synchronous calls to complete.
