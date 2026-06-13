# Portals And API Keys

Dense-Mem has two portal surfaces:

| Portal | URL | Auth | Purpose |
| --- | --- | --- | --- |
| User portal | `http://127.0.0.1:8080/ui` | Dense-Mem API key | Current key/session, self telemetry, current-key rotation, and bounded team management for manager keys. |
| Control portal | `http://127.0.0.1:8090/` | `CONTROL_PORTAL_TOKEN` | Teams, profiles, profile roles, keys, security bans, control telemetry. |

Keep the control portal private. It is not meant to be a public admin site.

## User Portal

The user portal runs on the main Dense-Mem API server. It authenticates with the
same API key used by MCP clients:

```text
Authorization: Bearer dm_...
```

Use it to:

- view the authenticated team and profile session
- rotate the current key when the key has write scope
- view self-scoped telemetry when telemetry is enabled
- manage same-team member profiles when the current key has manager role

The user portal cannot create arbitrary teams. Manager keys can update same-team
metadata and create, list, rename, rotate, or delete member profiles. The user
portal cannot create manager profiles or change roles; use the control portal
for manager lifecycle changes.

## User Portal SSO

When SSO is configured, the user portal adds public SSO endpoints under the main
Dense-Mem public base URL.

Register this redirect URI with the OIDC provider:

```text
https://<dense-mem-host>/ui/api/sso/callback
```

This value is derived from `SSO_PUBLIC_BASE_URL`, so a deployment with:

```text
SSO_PUBLIC_BASE_URL=https://dense-mem.example.com
```

uses:

```text
https://dense-mem.example.com/ui/api/sso/callback
```

Dense-Mem exposes these user-portal SSO endpoints:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/ui/api/sso/providers` | Lists enabled SSO providers for the login screen. |
| `GET` | `/ui/api/sso/start/:providerId` | Starts OIDC login and redirects to the provider. |
| `GET` | `/ui/api/sso/callback` | Receives the OIDC authorization code callback. |
| `POST` | `/ui/api/sso/logout` | Clears the local Dense-Mem SSO session cookies. |

Do not use `/ui/api/sso/logout` as an OIDC post-logout redirect URI. It is a
local API endpoint, not a browser landing page. Dense-Mem does not currently
perform RP-initiated OIDC logout with `post_logout_redirect_uri`.

If the provider requires a post-logout redirect URI, use the portal URL:

```text
https://<dense-mem-host>/ui
```

### SSO Login Visibility

The user portal shows SSO login only when public SSO is ready:

- `SSO_PUBLIC_BASE_URL` is an absolute `http` or `https` URL.
- At least one SSO provider is enabled.
- The provider has a valid issuer URL and client ID.
- If the provider uses `Client secret env`, that environment variable exists
  and is not empty.

When these conditions are not met, `/ui` should only show API-key login and
`GET /ui/api/sso/providers` should return no public providers. This is
intentional for deployments such as the hosted demo.

### Provider Fields

Configure providers in the control portal SSO section.

| Field | Meaning |
| --- | --- |
| Name | Display name shown on the user portal login screen. |
| Kind | Use `OIDC` for standards-based OIDC providers. Use provider-specific kinds only when Dense-Mem has a provider-specific integration. |
| Issuer URL | Provider issuer base URL, without `/.well-known/openid-configuration`. |
| Client ID | OIDC application client ID. |
| Client secret env | Environment variable name that contains the client secret. Leave empty for public PKCE clients. |
| Scopes | Login scopes requested during the browser flow. Use at least `openid`, `profile`, and `email`. |
| Group claims | Claim names that contain group, role, or entitlement values. |
| Groups endpoint | Optional endpoint used when group claims are not present or when bearer-key entitlement refresh needs a provider lookup. |
| Groups scopes | Scopes for the groups endpoint. If a groups endpoint is configured, these are also requested during browser login. |
| Enabled | Enables the provider for login after the rest of the setup is correct. |

Group mappings connect provider group values to Dense-Mem teams:

| Mapping field | Meaning |
| --- | --- |
| Team | Dense-Mem team to grant access to. |
| Group | Exact group or role value from the provider claim. |
| Permission | Dense-Mem API scopes, such as read-only or read/write. |
| Role | Dense-Mem profile role, such as member or manager. |
| Status | Disable a mapping without deleting it. |

### Generic OIDC Group And Role Mapping

Dense-Mem is not tied to a specific SSO vendor. Any OIDC provider can work when
it can provide stable group, role, or entitlement values through one of these
paths:

- ID-token claims
- UserInfo claims
- a configured groups endpoint

Set `Group claims` to the claim names that contain the values you want to map.
Dense-Mem accepts common claim shapes:

| Claim shape | Dense-Mem group values |
| --- | --- |
| `["admin", "writer"]` | `admin`, `writer` |
| `"admin"` | `admin` |
| `{ "admin": {...}, "writer": {...} }` | `admin`, `writer` |

The group mapping `Group` field must exactly match one emitted value. This can
be an IdP group name, a role key, an application role, or any stable entitlement
string your provider emits.

For claim-only providers, leave `Groups endpoint` and `Groups scopes` empty.
Claim-derived entitlements are cached for the browser SSO session lifetime. For
providers that require live entitlement refresh outside the browser session,
configure `Groups endpoint` and the delegated or client-credential scopes it
needs.

Examples:

| Provider style | Typical `Group claims` |
| --- | --- |
| Generic OIDC group claims | `groups` |
| Entra ID or Azure AD groups | `groups` or provider-specific group endpoint |
| Application roles | `roles` |
| ZITADEL project roles | `urn:zitadel:iam:org:project:<project_id>:roles, urn:zitadel:iam:org:project:roles` |

Use `LOG_LEVEL=debug` during setup and read `id_token_claim_names`,
`userinfo_claim_names`, and `groups` in the SSO callback logs to confirm the
right claim names and emitted values.

### Example: ZITADEL Cloud With Project Roles

For hosted ZITADEL, use the ZITADEL instance that owns the application as the
issuer URL:

```text
https://<instance>.us1.zitadel.cloud
```

If you have multiple ZITADEL URLs, use the one whose
`/.well-known/openid-configuration` issuer matches the application client ID.

In ZITADEL:

1. Create or open the project used for Dense-Mem.
2. Create a web application that uses Authorization Code with PKCE.
3. Add the Dense-Mem redirect URI:

   ```text
   https://<dense-mem-host>/ui/api/sso/callback
   ```

4. Copy the application client ID.
5. Create project roles for Dense-Mem access.

Example project role:

| ZITADEL role field | Example |
| --- | --- |
| Key | `dense-mem-admin` |
| Display name | `Dense-Mem Admin` |
| Group | `dense-mem` |

Dense-Mem matches the role key from the token claim. The ZITADEL role group is
only a ZITADEL-side organizer unless your emitted claim contains that value.

Assign the role to each user that should access Dense-Mem. For example, assign
your own user the `dense-mem-admin` role before testing login.

In Dense-Mem, configure the provider:

| Dense-Mem field | Value |
| --- | --- |
| Name | `ZITADEL` |
| Kind | `OIDC` |
| Issuer URL | `https://<instance>.us1.zitadel.cloud` |
| Client ID | ZITADEL application client ID |
| Client secret env | Empty for a public PKCE app, or an environment variable name for a confidential app secret. |
| Scopes | `openid, profile, email` |
| Group claims | `urn:zitadel:iam:org:project:<project_id>:roles, urn:zitadel:iam:org:project:roles` |
| Groups endpoint | Empty when using ZITADEL role claims. |
| Groups scopes | Empty when using ZITADEL role claims. |
| Enabled | Checked after the provider, roles, and mappings are ready. |

If ZITADEL does not emit role claims, enable `Assert Roles on Authentication`
on the project for UserInfo role claims, or `User Roles Inside ID Token` on the
application for ID-token role claims. When required by your ZITADEL setup, add
the project audience scope to `Scopes`:

```text
urn:zitadel:iam:org:project:id:<project_id>:aud
```

Create a Dense-Mem group mapping whose `Group` value is the ZITADEL role key:

| Dense-Mem mapping field | Example |
| --- | --- |
| Team | `Mark` |
| Group | `dense-mem-admin` |
| Permission | `Read/write` |
| Role | `Manager` |
| Status | `enabled` |

With debug logging enabled, a successful claim read should show the ZITADEL role
claim names and a group value matching the role key:

```json
{
  "configured_group_claims": [
    "urn:zitadel:iam:org:project:<project_id>:roles",
    "urn:zitadel:iam:org:project:roles"
  ],
  "groups": ["dense-mem-admin"],
  "groups_from_userinfo": true
}
```

### SSO Troubleshooting

Set `LOG_LEVEL=debug` while setting up SSO. The callback response now returns a
safe setup hint, and the server log has provider and claim details.

| Browser message | What to check |
| --- | --- |
| `sso setup failed: no groups found in configured claims` | `Group claims` does not match the token or UserInfo claim names, or the provider is not emitting the expected group or role claims. |
| `sso setup failed: group lookup failed` | The groups endpoint, delegated scopes, or client credentials are wrong. |
| `sso setup failed: no mapping matched the user groups` | The Dense-Mem mapping `Group` value does not exactly match the emitted role or group value. |
| `sso setup failed: no enabled team entitlement matched` | A mapping exists, but it is disabled or does not grant an enabled team entitlement. |
| `sso access denied` | The provider is disabled, the session is invalid, or entitlement validation failed after setup. |

For claim-based setups, the usual fix is:

1. Read `id_token_claim_names` and `userinfo_claim_names` in the debug log.
2. Put the provider's group or role claim names in `Group claims`.
3. Set the Dense-Mem mapping `Group` to the emitted group or role value.
4. Confirm the user is assigned that group, role, or entitlement in the IdP.

ZITADEL example references:

- [Retrieve User Roles in ZITADEL](https://zitadel.com/docs/guides/integrate/retrieve-user-roles)
- [Claims in ZITADEL](https://zitadel.com/docs/apis/openidoauth/claims)
- [Applications in ZITADEL](https://zitadel.com/docs/guides/manage/console/applications-overview)

## Control Portal

The control portal runs on a separate local port:

```text
http://127.0.0.1:8090/
```

It accepts either:

```text
Authorization: Bearer <CONTROL_PORTAL_TOKEN>
```

or:

```text
X-Control-Portal-Token: <CONTROL_PORTAL_TOKEN>
```

Use it to:

- create teams
- create named profiles
- set profile roles
- create read-only or read-write API keys
- rotate keys
- delete profiles or keys
- review usage and telemetry
- review or update IP ban settings

It does not browse or edit memory content.

## Operator CLI Commands

Create a team, default profile, and read-write key:

```bash
docker compose exec server /app/provision-team --name "primary-memory"
```

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

Delete a profile key:

```bash
docker compose exec server /app/delete-team-profile \
  --team-id "<team-id>" \
  --profile-id "<profile-id>"
```

## Roles And Scope Choices

Roles and scopes control different things:

| Field | Values | Controls |
| --- | --- | --- |
| Role | `manager`, `member` | Team/profile administration. |
| Scopes | `read`, `read` + `write` | Knowledge read/write behavior. |

The first profile in a new team defaults to `manager`. Later profiles default to
`member`. During migration, existing teams assign `manager` to the earliest
active profile and `member` to the rest.

Manager keys can access the team-management APIs and the `/ui` Team tab. Member
keys cannot, even when they have write scope.

| Key type | Use it for |
| --- | --- |
| Read-write | Main assistants that should remember, import, confirm, and mutate memory. |
| Read-only | Automation or tools that should recall and inspect memory but never write. |

Do not share write keys with tools that only need recall.

## API Example: Create A Read-Only Key

```bash
curl -X POST "http://127.0.0.1:8080/api/v1/teams/$TEAM_ID/profiles" \
  -H "Authorization: Bearer $MANAGER_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"automation-readonly","scopes":["read"],"rate_limit":120}'
```

The raw API key is returned once. Store it privately.
