<h1 align="center">Dense-Mem</h1>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Dense--Mem-trustworthy_AI_memory-0f766e?style=for-the-badge&logo=github&logoColor=white" alt="Dense-Mem" />
</p>

<p align="center">
  <strong>给 AI Agent 用的自托管长期记忆服务：有证据链、能识别冲突，不会悄悄覆盖事实。</strong>
</p>

<p align="center">
  <a href="https://demo-dense-mem.markhuang.ai"><img src="https://img.shields.io/badge/Try%20Dense--Mem%20live-Open%20hosted%20demo-0f766e?style=for-the-badge" alt="Try Dense-Mem live" /></a>
</p>

<p align="center">
  <strong>先开一个临时隔离团队在线体验，再决定是否自托管。</strong>
</p>

<p align="center">
  <a href="https://github.com/markhuangai/dense-mem"><img src="https://img.shields.io/github/stars/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub stars" /></a>
  <a href="https://github.com/markhuangai/dense-mem/issues"><img src="https://img.shields.io/github/issues/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub issues" /></a>
  <a href="https://github.com/markhuangai/dense-mem/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-Apache--2.0-blue?style=flat-square" alt="License: Apache-2.0" /></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.26" />
  <a href="https://github.com/markhuangai/dense-mem/pkgs/container/dense-mem"><img src="https://img.shields.io/badge/Docker-GHCR-2496ED?style=flat-square&logo=docker&logoColor=white" alt="Docker image on GHCR" /></a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/MCP-Streamable_HTTP-111827?style=flat-square" alt="MCP Streamable HTTP" />
  <img src="https://img.shields.io/badge/Neo4j-5.26-008CC1?style=flat-square&logo=neo4j&logoColor=white" alt="Neo4j 5.26" />
  <img src="https://img.shields.io/badge/PostgreSQL-18-4169E1?style=flat-square&logo=postgresql&logoColor=white" alt="PostgreSQL 18" />
  <img src="https://img.shields.io/badge/OpenAPI-3.0-6BA539?style=flat-square&logo=openapiinitiative&logoColor=white" alt="OpenAPI 3.0" />
  <img src="https://visitor-badge.laobi.icu/badge?page_id=markhuangai.dense-mem&style=flat-square" alt="Visitors" />
</p>

<p align="center">
  <a href="https://zenodo.org/records/20519039"><img src="https://zenodo.org/badge/DOI/10.5281/zenodo.20519039.svg" alt="DOI: 10.5281/zenodo.20519039" /></a>
</p>

Dense-Mem 是一层给 MCP 客户端使用的持久记忆服务。它把来源、claims、facts、
验证流程、服务端 embeddings、recall、团队隔离、REST/OpenAPI 和带 token
保护的控制门户放在同一个自托管系统里。宿主 LLM 继续负责对话和判断；
Dense-Mem 负责把记忆状态存稳、管住，并返回可以解释给用户的结构化结果。

从部署形态看，Dense-Mem 是一个独立的 HTTP MCP memory server。当前 v1
支持的 MCP 传输是 HTTP MCP，由主 HTTP 进程在 `/mcp` 暴露。

Dense-Mem 也是这篇研究预印本的一部分：
[Governed Enterprise AI Memory Beyond RAG: From Vector Retrieval to Permissioned
Knowledge Graphs](https://zenodo.org/records/20519039)。

## 项目介绍视频

<p align="center">
  <a href="https://cdn.markhuang.ai/videos/dense-mem/intro.mp4" target="_blank" rel="noopener noreferrer">
    <img src="assets/thumbnail.png" alt="观看 Dense-Mem 项目介绍视频" width="100%" />
  </a>
</p>

<p align="center">
  <a href="https://cdn.markhuang.ai/videos/dense-mem/intro.mp4" target="_blank" rel="noopener noreferrer"><strong>观看 Dense-Mem 项目介绍视频</strong></a>
</p>

## 在线体验

不用先部署。打开
[https://demo-dense-mem.markhuang.ai](https://demo-dense-mem.markhuang.ai)，
创建一个临时隔离团队，就可以先试 Dense-Mem 的核心流程。

<p align="center">
  <img src="assets/readme-hero.jpg" alt="Cartoon architecture illustration: AI clients send evidence into a secure Dense-Mem vault where claims become facts, conflicts become clarification questions, and durable storage sits behind the service." />
</p>

## 为什么需要 Dense-Mem？

AI Agent 需要的不是“能搜到一段相似文本”，而是日后仍然说得清、查得到、改得对的记忆。

- 证据先入库。记忆先以 source fragments 保存，再进入 claims 和 facts 流程。
- Facts 不是随手写入的文本，而是经过类型化 claims、验证和 promotion gates 后产生。
- 遇到可比较的冲突时，Dense-Mem 返回 `clarifications[]`，不会静默覆盖 active facts。
- 宿主 LLM 负责从对话中抽取候选记忆、向用户追问；Dense-Mem 负责持久状态、gates、审计元数据和 recall。
- 运维方保留对存储、team/profile 隔离、API keys 和数据出站边界的控制。

## 60 秒快速开始

下载本地基础 compose 示例和环境变量模板，填好必需的 secrets，然后启动服务：

```bash
mkdir dense-mem-local
cd dense-mem-local

curl -fsSLo docker-compose.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.base.yml
curl -fsSLo .env.example \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/.env.example

cp .env.example .env
# 填写 POSTGRES_PASSWORD、NEO4J_PASSWORD、CONTROL_PORTAL_TOKEN 和 AI_API_KEY。
${EDITOR:-vi} .env

docker compose up -d
docker compose exec server /app/provision-team --name "primary-memory"
```

基础 compose 示例会启动 Postgres、带 Neo4j Graph Data Science 插件的
`neo4j:5.26-community`，以及 Dense-Mem server。默认只暴露本机端口：

```text
MCP/API:        http://127.0.0.1:8080/mcp
User portal:    http://127.0.0.1:8080/ui
Control portal: http://127.0.0.1:8090/
```

首次拉取镜像可能不止 60 秒。基础示例刻意不包含 Redis 和公网 HTTPS；如果需要
这些部署能力，请使用 expert 示例。

服务启动时必须有完整的 embedding 配置：`AI_API_URL`、`AI_API_KEY`、
`AI_API_EMBEDDING_MODEL` 和 `AI_API_EMBEDDING_DIMENSIONS`。compose 示例已经为
URL、model 和 dimensions 提供 OpenAI 默认值：`https://api.openai.com/v1`、
`text-embedding-3-small`、`1536`。因此最小本地部署只需要补上 `AI_API_KEY`。
如果切换到其他 embedding provider 或 model，请一起覆盖这些配置。

Verifier 调用默认发送 `temperature: 0`。如果 provider 或 model 拒绝
temperature 字段，设置 `AI_VERIFIER_DISABLE_TEMPERATURE=true` 可以省略该字段。

### Telemetry Overlay

Prometheus telemetry 默认关闭，是可选功能。要为 `/ui` 应用和 control portal
dashboards 收集 usage、performance、verifier token、embedding token、recall
和 promotion metrics，可以把基础 stack 和 telemetry overlay 一起启动：

```bash
curl -fsSLo prometheus.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.yml
curl -fsSLo docker-compose.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml up -d
```

这个 overlay 会在 `127.0.0.1:9090` 启动 Prometheus，保留 30 天样本，把
`TELEMETRY_SCRAPE_TOKEN` 作为 scrape secret 传给 Prometheus，并让 Dense-Mem
使用 `http://prometheus:9090` 查询 telemetry。它也会设置
`TELEMETRY_PROMETHEUS_JOB=dense-mem`，这样在共享 Prometheus 时 dashboard
只查询 `dense-mem` scrape job。

在线 recall quality 卡片使用 `densemem_recall_feedback_total` 和
`densemem_recall_feedback_quality_score`。在 control portal config panel
开启 recall feedback，并且宿主 LLM 为 `recall_memory` 结果提交 compact
feedback 之前，这些卡片会保持为零。正常生产 recall 流量仍然会贡献请求量、
结果数和延迟指标。

对于一次性 demo image，保持 control portal 关闭，改用 demo telemetry
overlay：

```bash
curl -fsSLo prometheus.demo.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.demo.yml
curl -fsSLo docker-compose.demo.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.demo.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.demo.telemetry.yml up -d
```

demo overlay 会在私有 Compose network 上 scrape `demo:8091`，并设置
`TELEMETRY_PROMETHEUS_JOB=dense-mem-demo`。不要把这个 metrics listener
公开到公网。

## 能力对比

| 能力 | Dense-Mem | 文件记忆 | Vector DB | 通用 MCP memory |
|------|-----------|----------|-----------|-----------------|
| 证据来源 | Source fragments 先于 claims 和 facts 保存 | 通常缺失或只靠约定 | 保存 chunks，不保存事实演变 | 取决于实现 |
| Fact 变更 | 通过验证门和 promotion 规则控制 | 手工改文本 | 相似度更新容易掩盖历史 | 往往由工具自行处理 |
| 冲突处理 | 可比较冲突会返回 clarification tasks | 调用方自己发现 | 向量相似不代表语义矛盾 | 通常交给调用方 |
| Recall | 返回 facts、claims、fragments、contradictions 和 clarifications | 文本搜索 | 向量相似检索 | 取决于实现 |
| Agent 边界 | 宿主 LLM 做判断；Dense-Mem 做存储和约束 | 边界模糊 | 只负责检索 | 常常边界模糊 |
| 运维 | Teams、profiles、API keys、审计元数据、REST、OpenAPI、MCP | 很少 | 数据库运维 | 取决于实现 |

单节点部署可以不使用 Redis；多实例部署需要 Redis。

## 文档

README 只是产品概览。完整用户文档在
[Dense-Mem wiki](https://github.com/markhuangai/dense-mem/wiki)：

| 目标 | Wiki 页面 |
|------|-----------|
| 本地跑起来 | [Quick Start](https://github.com/markhuangai/dense-mem/wiki/Quick-Start) |
| 日常使用记忆功能 | [Using Dense-Mem](https://github.com/markhuangai/dense-mem/wiki/Using-Dense-Mem) |
| 配置 providers、Redis 和 Traefik | [Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration) |
| 理解系统架构 | [Architecture](https://github.com/markhuangai/dense-mem/wiki/Architecture) |
| 查看 API 和运维细节 | [Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference) |

## 职责边界

| 范围 | Dense-Mem 负责 | 宿主 LLM 负责 |
|------|----------------|---------------|
| 写入记忆 | Evidence fragments、类型化 claims、verification、gates、promotion | 从聊天文本中抽取候选记忆 |
| Embeddings | 通过配置的 provider 生成 fragment embeddings 和 recall-query embeddings | 正常写入和 recall 不需要自己传 vectors |
| Retrieval | Facts、validated claims、fragments、contradictions、clarification tasks | 决定对话中要问什么、引用什么 |
| Truth changes | 可比较冲突检测，以及用户确认后的 supersession | 向用户确认哪条不确定记忆为准 |
| Operations | Teams、named profiles、API keys、审计元数据、control portal | 客户端 MCP 配置 |

Dense-Mem 不是 agent brain、planner，也不是外部真相裁判。它负责存储记忆、
执行明确的 gates，并返回结构化结果。

## 记忆工作流

| Tool | 用途 |
|------|------|
| `remember` | 常规聊天记忆写入：保存证据、创建类型化 claims、验证，在 gates 通过时 promotion，并返回结构化结果。 |
| `import_memories` | 导入整理过的历史对话。默认只记录证据和 validated claims，不自动 promotion。 |
| `recall_memory` | 为已认证团队检索 facts、validated claims、fragments 和 `clarifications[]`。 |
| `reflect_memories` | 查看 active facts、candidate/disputed claims、contradictions、stale memories 和 clarification needs。 |
| `confirm_memory` | 应用用户对 clarification task 的回答：接受 claim 并 supersede 可比较的 active facts，或保留/拒绝它。 |

高级调用方仍然可以使用低层工具：`post_claim`、`verify_claim`、`promote_claim`、
搜索工具、graph query tools、community tools 和 retraction tools。

记忆在系统里的主路径如下：

```text
source fragment -> typed claim -> verification -> promotion gate -> active fact
                                                   |
                                                   v
                                            clarification task
```

可比较冲突不会被静默解决。Dense-Mem 返回 `clarifications[]`，宿主 LLM
再向用户确认哪条记忆正确。用户回答后，宿主调用 `confirm_memory`。

## 数据出站

Dense-Mem 会把 fragment text 和 recall queries 发给配置的 embedding
provider。Claim verification 可能会把 candidate claims 和 supporting evidence
发给配置的 verifier provider。自托管 providers 可以把这些流量留在你的边界内；
托管 providers 则不在你的自有边界内。provider 设置和数据出站细节见 wiki：
[Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration) 和
[Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference)。

## Embedding Model 一致性

Dense-Mem 负责正常写入和 recall 的 embeddings。启动时它会检查已存储的
embedding model 和 dimension，避免不同模型或维度的 vectors 被混在一起。
模型轮换需要重新 embedding 或重建 vector indexes；具体步骤放在 wiki 的
[Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration)。

## Tool Discoverability

Dense-Mem 用同一个 registry 支撑三种 discoverability surface：

| Surface | Path | Purpose |
|---------|------|---------|
| Tool catalog | `GET /api/v1/tools` | 运行时 tool discovery |
| Runtime OpenAPI | `GET /api/v1/openapi.json` | Agents、codegen、integrations |
| MCP Streamable HTTP | `POST /mcp`, `GET /mcp` | 主 HTTP 服务上的 MCP clients |

完整 route list 和 client examples 在 wiki：
[Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference)
和 [Quick Start](https://github.com/markhuangai/dense-mem/wiki/Quick-Start)。

## 设计说明

- [standalone MCP memory architecture](https://github.com/markhuangai/dense-mem/wiki/Standalone-MCP-Memory-Architecture)
- [knowledge-pipeline contracts](https://github.com/markhuangai/dense-mem/wiki/Knowledge-Pipeline-Contracts)
- [knowledge-pipeline client contracts](https://github.com/markhuangai/dense-mem/wiki/Knowledge-Pipeline-Client-Contracts)
- [knowledge-pipeline operability](https://github.com/markhuangai/dense-mem/wiki/Knowledge-Pipeline-Operability)

## License

Apache-2.0
