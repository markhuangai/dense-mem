# Quick Start

This page gets Dense-Mem running locally with the fewest decisions.

Use this path first. After it works, you can review [Configuration](Configuration)
for advanced settings.

## What You Will Set Up

You will run:

- Dense-Mem server
- Postgres for teams, profiles, keys, and audit records
- Neo4j for memory graph and search indexes
- a local control portal for creating API keys

Redis and public HTTPS are optional. Skip them for your first setup.

Dense-Mem provides two downloadable compose examples:

| Example | Use it when |
| --- | --- |
| [docker-compose.base.yml](https://github.com/markhuangai/dense-mem/blob/main/examples/docker-compose.base.yml) | You want the easiest local-only setup. Start here. |
| [docker-compose.expert.yml](https://github.com/markhuangai/dense-mem/blob/main/examples/docker-compose.expert.yml) | You need public HTTPS, Traefik, Redis, or advanced edge settings. |

## Step 1: Get The Files

Create a folder for Dense-Mem:

```bash
mkdir dense-mem-local
cd dense-mem-local
```

Download the [base local-only compose file](https://github.com/markhuangai/dense-mem/blob/main/examples/docker-compose.base.yml)
and the [env template](https://github.com/markhuangai/dense-mem/blob/main/examples/.env.example):

```bash
curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.base.yml
curl -fsSLo .env.example \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/.env.example
```

Create your local `.env` from the template:

```bash
cp .env.example .env
```

The base compose file keeps Dense-Mem local to your machine. It does not publish
the service to the internet.

## Step 2: Fill In The Required Values

Open `.env` and replace the placeholder values:

```bash
${EDITOR:-vi} .env
```

At minimum, fill in:

```text
POSTGRES_PASSWORD=choose-a-strong-postgres-password
NEO4J_PASSWORD=choose-a-strong-neo4j-password
CONTROL_PORTAL_TOKEN=choose-a-long-control-portal-token
AI_API_KEY=your-ai-provider-api-key
```

The control portal creates and rotates API keys. The base compose file binds it
to `127.0.0.1`, so it is reachable only from your computer.

The server requires a complete embedding configuration at startup:
`AI_API_URL`, `AI_API_KEY`, `AI_API_EMBEDDING_MODEL`, and
`AI_API_EMBEDDING_DIMENSIONS`. The compose examples provide OpenAI defaults for
the URL, model, and dimensions (`https://api.openai.com/v1`,
`text-embedding-3-small`, `1536`), so the first setup only needs you to fill in
`AI_API_KEY`. Override those values together when using a different embedding
provider or model.

## Step 3: Start Dense-Mem

Run:

```bash
docker compose up -d
```

Check that the containers are running:

```bash
docker compose ps
```

The local MCP endpoint is now reachable from your computer:

```text
http://localhost:8080/mcp
```

The local control portal is:

```text
http://127.0.0.1:8090/
```

## Step 4: Create Your First API Key

Create a team and profile key:

```bash
docker compose exec server /app/provision-team --name "primary-memory"
```

The command prints an API key once. Save it somewhere private.

The output looks like this:

```json
{
  "team_id": "11111111-2222-3333-4444-555555555555",
  "team_name": "primary-memory",
  "profile_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
  "profile_name": "default profile",
  "scopes": ["read", "write"],
  "api_key": "dm_default-prof_..."
}
```

## Step 5: Connect An MCP Client

Set the key in your terminal:

```bash
export DENSE_MEM_API_KEY="dm_default-prof_..."
```

### Claude Code

```bash
claude mcp add --transport http dense-mem http://localhost:8080/mcp \
  --header "Authorization: Bearer $DENSE_MEM_API_KEY"
```

### Codex

Add this to `~/.codex/config.toml`:

```toml
[mcp_servers.dense_mem]
url = "http://localhost:8080/mcp"
bearer_token_env_var = "DENSE_MEM_API_KEY"
tool_timeout_sec = 60
enabled = true
```

## Step 6: Try A Memory

Ask your MCP client to remember something small:

```text
Remember that I prefer concise explanations with concrete examples.
```

Then ask:

```text
What do you remember about my explanation preferences?
```

If your client is wired correctly, it should call Dense-Mem to save and recall
the memory.

## Stop Or Restart

Stop the local stack:

```bash
docker compose down
```

Start it again:

```bash
docker compose up -d
```

Your data stays in Docker volumes unless you intentionally remove those volumes.

## Optional: Expert Compose

Use the [expert compose file](https://github.com/markhuangai/dense-mem/blob/main/examples/docker-compose.expert.yml) only when you intentionally need public HTTPS, Traefik, Redis, or edge limits:

```bash
curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.expert.yml
```

Read [Configuration](Configuration) before using the expert example.

## Next Step

Read [Using Dense-Mem](Using-Dense-Mem) for everyday use patterns and examples.
