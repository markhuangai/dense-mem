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
  <img src="https://img.shields.io/badge/PostgreSQL-18-4169E1?style=flat-square&logo=postgresql&logoColor=white" alt="PostgreSQL 18" />
  <img src="https://visitor-badge.laobi.icu/badge?page_id=markhuangai.dense-mem&style=flat-square" alt="Visitors" />
</p>

<p align="center">
  <a href="https://doi.org/10.5281/zenodo.21403316"><img src="https://zenodo.org/badge/DOI/10.5281/zenodo.21403316.svg" alt="DOI: 10.5281/zenodo.21403316" /></a>
</p>

Dense-Mem 是一层给 MCP 客户端使用的持久记忆服务。它把来源、claims、facts、
验证流程、服务端 embeddings、recall、团队隔离、MCP endpoint、用户门户和带
token 保护的控制门户放在同一个自托管系统里。宿主 LLM 继续负责对话和判断；
Dense-Mem 负责把记忆状态存稳、管住，并返回可以解释给用户的结构化结果。

从部署形态看，Dense-Mem 是一个独立的 HTTP MCP memory server。当前 v1
支持的 MCP 传输是 HTTP MCP，由主 HTTP 进程在 `/mcp` 暴露。

Dense-Mem 也是这篇研究预印本的一部分：
[Governed Enterprise AI Memory Beyond RAG: From Vector Retrieval to Permissioned
Knowledge Graphs](https://zenodo.org/records/21403316)。

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
# 填写 POSTGRES_PASSWORD、CONTROL_PORTAL_TOKEN 和 AI_API_KEY。
${EDITOR:-vi} .env

docker compose up -d
docker compose exec server /app/provision-team --name "primary-memory"
```

基础 compose 示例会启动带 pgvector 的 Postgres 和 Dense-Mem server。
默认只暴露本机端口：

```text
MCP/API:        http://127.0.0.1:8080/mcp
User portal:    http://127.0.0.1:8080/ui
Control portal: http://127.0.0.1:8090/
```

首次拉取镜像可能不止 60 秒。基础示例刻意不包含 Redis 和公网 HTTPS；如果需要
这些部署能力，请使用 expert 示例。

服务启动时必须有完整的 embedding 和 assessor 配置：`AI_API_URL`、
`AI_API_KEY`、`AI_API_EMBEDDING_MODEL`、`AI_API_EMBEDDING_DIMENSIONS`、
`AI_VERIFIER_MODEL`。compose 示例已经为 embedding URL、
model 和 dimensions 提供 OpenAI 默认值：`https://api.openai.com/v1`、
`text-embedding-3-large`、`3072`，但 chat model 需要在 `.env` 里显式选择。
如果切换到其他 embedding provider 或 model，请一起覆盖这些配置。

Verifier 调用默认发送 `temperature: 0`。如果 provider 或 model 拒绝
temperature 字段，设置 `AI_VERIFIER_DISABLE_TEMPERATURE=true` 可以省略该字段。

### 完全本地部署（Ollama）

Dense-Mem 也可以完全不依赖托管 AI provider 运行。任何 OpenAI 兼容端点都能提供
embedding 和 verification；如果 Docker 宿主机上运行着
[Ollama](https://ollama.com)，先拉取两个模型，再把 `.env` 指向它们：

```bash
ollama pull nomic-embed-text
ollama pull llama3.1:8b
```

```text
AI_API_URL=http://host.docker.internal:11434/v1
AI_API_KEY=ollama
AI_API_EMBEDDING_MODEL=nomic-embed-text
AI_API_EMBEDDING_DIMENSIONS=768
AI_VERIFIER_MODEL=llama3.1:8b
AI_VERIFIER_TIMEOUT_SECONDS=300
```

这条路径上有三个关键细节：

- 使用 `host.docker.internal` 而不是 `127.0.0.1`；server 是从 compose 网络内部
  调用 provider 的。基础 compose 示例已经把该域名映射到 Docker 宿主机，因此在
  Linux（Docker 默认不定义该域名）上同样可用。
- `AI_API_KEY` 必须非空，即使 Ollama 会忽略它；启动校验要求完整的 embedding
  配置。
- `AI_VERIFIER_MODEL` 要设为 chat endpoint 上真实存在的模型。启动校验会在开始
  写入 memory 前发现缺失或错误配置。7B-8B 级别模型适合本地 smoke test；更大的
  模型在加载期间可能超过默认 60 秒超时，placement 会保持可重试直到模型可响应。

### 你的第一条记忆

`remember` 存储 evidence 后立即返回 `ingest_id`；placement 是异步完成的。用该
`ingest_id` 轮询 `get_memory_placement`，查看这条记忆的去向：

| Category | 含义 |
|----------|------|
| `promoted_fact` | claim 被提取、验证并晋升为 active fact。 |
| `validated_claim` | claim 通过了验证，但没有生成 fact。 |
| `candidate_claim` | claim 被搁置，等待更强的支持；verifier 失败时错误在 item 的 `error` 字段里。 |
| `fragment_only` | evidence 以可检索的形式存储，没有生成 typed claim。 |
| `needs_more_evidence` | placement 需要更多 evidence 才能决定。 |
| `rejected_false` | evidence 看起来是一条矛盾或 false-memory 更正。 |

服务端的 claim 提取刻意保持保守：像 "I prefer ..."、"I like ..."、"I use ..."
这样简单的第一人称陈述，是首次运行时看到 evidence 到 fact 完整路径的可靠方式。
其他表述——包括 "Josh prefers ..." 这类第三人称形式——会作为 `fragment_only`
的 evidence 保留，recall 仍会返回它们，只是排序低于 facts。`fragment_only`
是正常结果，不是错误。

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
开启 recall feedback，并且宿主 LLM 为 `recall_memory` 结果提交 feedback
之前，这些卡片会保持为零。只有质量为 `high` 且没有负向 flags 的 feedback
可以省略 `feedback_comment`；`medium`、`low` 或带负向 flags 的 feedback
需要有界 comment，也可以包含 irrelevant result refs，供离线分析使用。
Prometheus 仍然只接收有界 labels；自由文本 comment 保存在 recall feedback
investigation records 中。正常生产 recall 流量仍然会贡献请求量、结果数和延迟指标。

## 能力对比

| 能力 | Dense-Mem | 文件记忆 | Vector DB | 通用 MCP memory |
|------|-----------|----------|-----------|-----------------|
| 证据来源 | Source fragments 先于 claims 和 facts 保存 | 通常缺失或只靠约定 | 保存 chunks，不保存事实演变 | 取决于实现 |
| Fact 变更 | 通过验证门和 promotion 规则控制 | 手工改文本 | 相似度更新容易掩盖历史 | 往往由工具自行处理 |
| 冲突处理 | 可比较冲突会返回 clarification tasks | 调用方自己发现 | 向量相似不代表语义矛盾 | 通常交给调用方 |
| Recall | 返回 facts、claims、fragments、contradictions 和 clarifications | 文本搜索 | 向量相似检索 | 取决于实现 |
| Agent 边界 | 宿主 LLM 做判断；Dense-Mem 做存储和约束 | 边界模糊 | 只负责检索 | 常常边界模糊 |
| 运维 | Teams、profiles、API keys、审计元数据、MCP、浏览器和控制 APIs | 很少 | 数据库运维 | 取决于实现 |

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
| `remember` | 常规聊天记忆写入：只保存证据，并返回一个 placement run 交给 Dense-Mem verifier 处理。 |
| `get_memory_placement` | 轮询 `remember` 返回的 verifier placement run，包括 fragment-only、claim、fact、rejected 和 needs-evidence 等结果。 |
| `dispute_memory_placement` | 使用额外证据发起或继续有界的 placement dispute；由 verifier 决定是 promotion 还是保持 rejected。 |
| `import_memories` | 整理过的历史对话的可信迁移路径。可以携带显式 claims，并可请求自动 promotion。 |
| `recall_memory` | 为已认证团队检索 facts、validated claims、fragments、`clarifications[]` 以及仅假设性的 `related_dreams`。 |
| `resolve_dream_feedback` | 记录 dream 相关决策。`ignore` 保留 dream 供后续 recall；确认为真或假的 dream 进入正常记忆放置流程，并从后续 dream recall 中移除。 |
| `trace_memory` | 将一个 fact 或 claim 展开为有界的证据、promotion 来源链、contradictions 和 supersession 链接。 |
| `assemble_context` | 构建一个有界的、可直接放入 prompt 的上下文块，以及结构化的 facts、claims、fragments 和 clarifications。 |
| `reflect_memories` | 查看 active facts、candidate/disputed claims、contradictions、stale memories 和 clarification needs。 |
| `confirm_memory` | 应用用户对 clarification task 的回答：接受 claim 并 supersede 可比较的 active facts，或保留/拒绝它。 |
| `find_memory_pack_candidates` | 查找可以导出为 portable memory pack 的 facts 和 validated claims。 |
| `export_memory_pack` | 把选中的记忆导出成带 SHA-256 integrity hash 的 canonical JSON artifact，便于审阅或分享。 |
| `inspect_memory_pack` | 解析 memory-pack artifact 或 URL，并在不写入记忆的情况下报告 duplicates、conflicts 和 required decisions。 |
| `import_memory_pack` | 以 review 或 trusted 模式导入 memory pack，并记录可回滚的变更 ledger。 |
| `rollback_memory_pack_import` | 在 ledger 状态足够时回滚一次 memory-pack import 的变更。 |

高级调用方仍然可以使用低层工具：`post_claim`、`verify_claim`、`promote_claim`、
搜索工具、graph query tools、community tools 和 retraction tools。

旧的 `*_skill_pack*` tool names 仍会作为隐藏兼容 alias 被接受，但新客户端应使用
`*_memory_pack*`。Dense-Mem 也通过 `prompts/list` 和 `prompts/get` 暴露 MCP
prompts；首个内置 prompt `export_memory_as_agent_skill` 会引导 LLM recall
Dense-Mem 经验，并草拟自包含、可分享的 Agent Skill `SKILL.md` 文件，
让没有源 memory instance 访问权限的接收方也能使用，且不依赖 memory-pack
import/export。

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

Dense-Mem 从服务端 tool executor 使用的同一个 runtime registry 暴露 MCP
discovery：

| Surface | Path | Purpose |
|---------|------|---------|
| MCP Streamable HTTP | `POST /mcp`, `GET /mcp` | 主 HTTP 服务上的 MCP clients，包含 tools 和内置 prompts |

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
