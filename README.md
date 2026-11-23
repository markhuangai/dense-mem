<h1 align="center">Dense-Mem</h1>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Dense--Mem-governed_AI_memory-0f766e?style=for-the-badge&logo=github&logoColor=white" alt="Dense-Mem" />
</p>

<p align="center">
  <strong>Self-hosted MCP memory with exact Evidence, provenance, and governed recall.</strong>
</p>

<p align="center">
  <a href="https://demo-dense-mem.markhuang.ai"><img src="https://img.shields.io/badge/Try%20Dense--Mem%20live-Open%20hosted%20demo-0f766e?style=for-the-badge" alt="Try Dense-Mem live" /></a>
</p>

<p align="center">
  <a href="https://github.com/markhuangai/dense-mem"><img src="https://img.shields.io/github/stars/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub stars" /></a>
  <a href="https://github.com/markhuangai/dense-mem/issues"><img src="https://img.shields.io/github/issues/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub issues" /></a>
  <a href="https://github.com/markhuangai/dense-mem/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-Apache--2.0-blue?style=flat-square" alt="License: Apache-2.0" /></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.26" />
  <a href="https://github.com/markhuangai/dense-mem/pkgs/container/dense-mem"><img src="https://img.shields.io/badge/Docker-GHCR-2496ED?style=flat-square&logo=docker&logoColor=white" alt="Docker image on GHCR" /></a>
</p>

<p align="center">
  <a href="https://markhuangai.github.io/dense-mem/">Interactive documentation</a> ·
  <a href="https://markhuangai.github.io/dense-mem/Quick-Start/">Setup guide</a> ·
  <a href="https://markhuangai.github.io/dense-mem/Architecture/">How it works</a>
</p>

Dense-Mem is a standalone HTTP MCP memory server using Streamable HTTP. AI
clients choose what to remember; Dense-Mem stores exact Evidence, preserves its
sources and lifecycle, and retrieves authorized memory across conversations.

PostgreSQL with pgvector is the durable authority. Redis is coordination only:
a single-node deployment can use process memory; a multi-instance deployment
requires Redis or equivalent distributed coordination.

## Try the demo

[Open the hosted demo](https://demo-dense-mem.markhuang.ai) to create a temporary,
isolated team and try disposable memory before self-hosting.

<p align="center">
  <img src="assets/readme-hero.jpg" alt="AI clients submit Evidence; Dense-Mem preserves provenance and lifecycle and returns authorized memory." />
</p>

## Why Dense-Mem

- **Keep the source.** Exact Evidence retains occurrences, source revisions,
  provenance, and an append-only lifecycle.
- **Control access.** Private and team-shared memory have explicit destinations;
  shared visibility does not grant permission to change another owner's Evidence.
- **Follow the history.** Recall returns eligible Evidence; Trace explains its
  provenance, and corrections or retractions preserve lineage.
- **Organize without inventing facts.** Derived topics help discovery while
  Evidence remains canonical memory.

See [Using Dense-Mem](https://markhuangai.github.io/dense-mem/Using-Dense-Mem/)
for the memory workflow and
[Architecture](https://markhuangai.github.io/dense-mem/Architecture/) for the
system design.

## Quickstart

Install Docker with Compose. This fresh-install example pins the image and
configuration files to the same published release:

```bash
mkdir dense-mem-local
cd dense-mem-local
export DENSE_MEM_VERSION=v2.8.0
export DENSE_MEM_IMAGE="ghcr.io/markhuangai/dense-mem:${DENSE_MEM_VERSION}"

curl -fsSLo docker-compose.yml \
  "https://raw.githubusercontent.com/markhuangai/dense-mem/${DENSE_MEM_VERSION}/examples/docker-compose.base.yml"
curl -fsSLo .env.example \
  "https://raw.githubusercontent.com/markhuangai/dense-mem/${DENSE_MEM_VERSION}/examples/.env.example"
cp .env.example .env
${EDITOR:-vi} .env
```

Set these values in `.env`:

| Setting | Purpose |
| --- | --- |
| `POSTGRES_PASSWORD` | Password for the local PostgreSQL instance |
| `CONTROL_PORTAL_TOKEN` | Private control-portal access token |
| `AI_API_KEY` | Model-provider credential |
| `AI_VERIFIER_MODEL` | Chat model used to assess Evidence |

The example supplies OpenAI embedding defaults. For other providers, change the
endpoint, model, and dimensions together; see
[Configuration](https://markhuangai.github.io/dense-mem/Configuration/).

```bash
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8080/ready
```

Startup applies migrations before readiness. Keep the image pin for later
starts; consult the
[Operations Runbook](https://markhuangai.github.io/dense-mem/Operations-Runbook/)
before changing an existing database.

## Connect your AI client

1. Open the private control portal at `http://127.0.0.1:8090/` with
   `CONTROL_PORTAL_TOKEN`. Create a team, then create its first credential.
2. Open the user portal at `http://127.0.0.1:8080/ui`. Its **Connect** page shows
   the MCP connection details. The local MCP endpoint is `http://127.0.0.1:8080/mcp`.
3. Configure your host using the
   [MCP client guide](https://markhuangai.github.io/dense-mem/MCP-Clients/),
   then try a Remember, Recall, and Trace workflow.

The control listener stays private. Configured hosted model providers can
receive Evidence and queries; use self-hosted providers when that traffic must
stay within your infrastructure. External memory automation uses `/mcp`.

## Documentation

| Goal | Guide |
| --- | --- |
| Learn how memory works | [Evidence walkthrough](https://markhuangai.github.io/dense-mem/) · [Using Dense-Mem](https://markhuangai.github.io/dense-mem/Using-Dense-Mem/) |
| Configure and operate a server | [Configuration](https://markhuangai.github.io/dense-mem/Configuration/) · [Operations](https://markhuangai.github.io/dense-mem/Operations-Runbook/) |
| Choose deployment examples | [Examples](examples/README.md) · [Grafana](examples/grafana/README.md) |
| Integrate a client | [Python / ADK / LangChain](packages/python/README.md) · [stdio MCP proxy](packages/mcp-proxy/README.md) |
| Develop and test | [Scripts](scripts/README.md) · [Evaluation](tests/eval/README.md) · [ADRs](adr/) |

The documentation site holds interactive walkthroughs and detailed reference.
Directory READMEs cover the files they accompany. For documented behavior
changes, link a companion `gh-pages` PR or explain why the site needs no update.

Dense-Mem is part of the research preprint
[Governed Enterprise AI Memory Beyond RAG: From Vector Retrieval to Permissioned
Knowledge Graphs](https://zenodo.org/records/21403316).

## License

[Apache-2.0](LICENSE)
