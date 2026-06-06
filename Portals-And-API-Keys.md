# Portals And API Keys

Dense-Mem has two portal surfaces:

| Portal | URL | Auth | Purpose |
| --- | --- | --- | --- |
| User portal | `http://127.0.0.1:8080/ui` | Dense-Mem API key | Current key/session, self telemetry, current-key rotation. |
| Control portal | `http://127.0.0.1:8090/` | `CONTROL_PORTAL_TOKEN` | Teams, profiles, keys, security bans, control telemetry. |

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

The user portal cannot create arbitrary teams or manage every key.

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

## Key Scope Choices

| Key type | Use it for |
| --- | --- |
| Read-write | Main assistants that should remember, import, confirm, and mutate memory. |
| Read-only | Automation or tools that should recall and inspect memory but never write. |

Do not share write keys with tools that only need recall.

## API Example: Create A Read-Only Key

```bash
curl -X POST "http://127.0.0.1:8080/api/v1/teams/$TEAM_ID/profiles" \
  -H "Authorization: Bearer $DENSE_MEM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"automation-readonly","scopes":["read"],"rate_limit":120}'
```

The raw API key is returned once. Store it privately.
