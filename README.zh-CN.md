<h1 align="center">Dense-Mem</h1>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Dense--Mem-governed_AI_memory-0f766e?style=for-the-badge&logo=github&logoColor=white" alt="Dense-Mem" />
</p>

<p align="center">
  <strong>面向 MCP 的自托管记忆服务：保留原始证据、显式管理生命周期、按支持路径检索。</strong>
</p>

<p align="center">
  <a href="https://demo-dense-mem.markhuang.ai"><img src="https://img.shields.io/badge/Try%20Dense--Mem%20live-Open%20hosted%20demo-0f766e?style=for-the-badge" alt="在线体验 Dense-Mem" /></a>
</p>

<p align="center">
  <a href="https://github.com/markhuangai/dense-mem"><img src="https://img.shields.io/github/stars/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub stars" /></a>
  <a href="https://github.com/markhuangai/dense-mem/issues"><img src="https://img.shields.io/github/issues/markhuangai/dense-mem?style=flat-square&logo=github" alt="GitHub issues" /></a>
  <a href="https://github.com/markhuangai/dense-mem/blob/main/LICENSE"><img src="https://img.shields.io/badge/License-Apache--2.0-blue?style=flat-square" alt="License: Apache-2.0" /></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go 1.26" />
  <a href="https://github.com/markhuangai/dense-mem/pkgs/container/dense-mem"><img src="https://img.shields.io/badge/Docker-GHCR-2496ED?style=flat-square&logo=docker&logoColor=white" alt="Docker image on GHCR" /></a>
</p>

Dense-Mem 是独立的、使用 Streamable HTTP 的 MCP 记忆服务器。它先持久化精确
证据，再通过经过校验的服务端策略派生语义状态，并返回仍然有效的证据上下文和图形化
Relationship 句柄。PostgreSQL 是知识、生命周期、来源、搜索、授权和审计的唯一持久
权威；Redis 只用于协调。单节点部署可使用进程内协调；多实例部署需要 Redis 或等效的
分布式协调实现。

宿主 LLM 负责对话和判断。Dense-Mem 负责证据持久化、所有者授权、生命周期事件、
支持有效性和有界检索。面向外部自动化的记忆契约只有 `/mcp`；浏览器路由是第一方
产品界面，不是另一套公共自动化 API。

Dense-Mem 也是这篇研究预印本的一部分：
[Governed Enterprise AI Memory Beyond RAG: From Vector Retrieval to Permissioned
Knowledge Graphs](https://zenodo.org/records/21403316)。

## 在线体验

打开 [https://demo-dense-mem.markhuang.ai](https://demo-dense-mem.markhuang.ai)，
创建临时隔离团队后可用一次性测试数据体验核心流程。

<p align="center">
  <img src="assets/readme-hero.jpg" alt="AI 客户端提交证据，受治理的记忆服务记录生命周期并返回带来源的活跃关系。" />
</p>

## 为什么使用 Dense-Mem

- 证据精确、可持久保存且只追加。生命周期动作只改变有效状态，不删除来源或追踪链路。
- Entity 和带类型的 Value 是语义节点。只有证据支持仍有效时，profile 所有的
  Relationship 才会成为活跃图边。
- Provider 输出只是提议。封闭 schema 校验和确定性的服务端策略决定持久状态。
- 默认检索排除候选项和 Hypothesis；只有对请求时间仍有效的 Relationship 支持路径
  存在时，才返回证据上下文。
- 团队可见性和 profile 修改权限不同。作者只能修改自己的证据和归属语义记录。

## 60 秒快速开始

下载本地 compose 示例和环境变量模板，配置必要密钥后启动：

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
```

基础 stack 使用带 pgvector 的 PostgreSQL 作为持久权威。正常运行时保持 `NEO4J_*`
未设置；遗留 Neo4j 语料只能作为迁移输入，不是运行时回退。默认本机端口：

```text
MCP:            http://127.0.0.1:8080/mcp
用户门户:       http://127.0.0.1:8080/ui
控制门户:       http://127.0.0.1:8090/
```

使用 `CONTROL_PORTAL_TOKEN` 打开控制门户，然后创建团队及其第一个 profile/API key。
控制面自动化使用同一个私有 API：

```bash
control_token="<.env 中的 CONTROL_PORTAL_TOKEN>"

curl -fsS -X POST http://127.0.0.1:8090/control/api/teams \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"primary-memory"}'

curl -fsS -X POST http://127.0.0.1:8090/control/api/teams/<team-id>/profiles \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"default profile"}'
```

正式镜像只包含一个项目可执行文件 `/app/server`。它会在开始提供服务前，借助数据库
session lock 自动执行待处理的 PostgreSQL migration，因此 Compose 不需要单独的
migration 容器。共享同一个可写 primary 的多个 server replica 会串行执行该启动步骤。
滚动发布期间，migration 必须与上一版应用保持向后兼容；彼此独立的数据库仍需分别由
连接到该数据库的 server 完成 migration。管理操作通过私有控制门户/API 完成；dreaming
和自动 conflict review 仍由 server 后台 worker 执行。

镜像 healthcheck 为默认的 30 分钟 migration 窗口保留启动宽限期，并在首次检查成功后
进入正常检测。若将 `POSTGRES_MIGRATION_TIMEOUT_SECONDS` 设为大于 1800，应把部署的
healthcheck start period 同步调整到至少相同时间。

RC 标签为 `vX.Y.Z-rc.N` 和 `demo-vX.Y.Z-rc.N`。稳定版标签为 `vX.Y.Z`、
`latest` 和 `demo-vX.Y.Z`；不提供滚动 demo 标签。

启动时必须配置完整的 embedding 和 verifier：`AI_API_URL`、
`AI_API_KEY`、`AI_API_EMBEDDING_MODEL`、`AI_API_EMBEDDING_DIMENSIONS`、
`AI_VERIFIER_MODEL`。compose 示例为 embedding 提供
OpenAI 默认值；chat model 需要在 `.env` 中明确选择。

Verifier 和 assessor 调用默认发送 `temperature: 0`。如果 provider 或 model
拒绝 temperature 字段，设置 `AI_VERIFIER_DISABLE_TEMPERATURE=true` 可以省略该字段。

### 完全本地部署（Ollama）

任何 OpenAI 兼容端点都可提供 embedding 和 verification。若 Docker 宿主机运行
[Ollama](https://ollama.com)：

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

请使用 `host.docker.internal`，不要使用 `127.0.0.1`，因为服务从 compose
网络内调用 provider。即使 Ollama 忽略它，`AI_API_KEY` 也必须非空，因为启动校验
要求完整的 provider 配置。

- `AI_VERIFIER_MODEL` 要设为 chat endpoint 上真实存在的模型。启动校验会在开始
  写入 memory 前发现缺失或错误配置。7B-8B 级别模型适合本地 smoke test；更大的
模型在加载期间可能超过默认 60 秒超时，处理会保持可重试直到模型可响应。

## 证据生命周期

`remember` 会先持久化精确证据并返回 `submission_id`；provider 调用和处理在确认
后继续执行。使用 `get_submission_status` 查询按所有者隔离的处理与搜索状态。
该状态投影不会暴露 placement 问题、provider 输出或内部 run ID。

若要替换自己拥有的一条当前证据，把其 UUID 放入新证据的
`supersedes_evidence_ids`。直接指定目标与通过 `previous_source_revision` 推进
来源修订是两条独立流程，不能混用。

```json
{
  "evidence": [
    {
      "content": "部署目标现在仅使用 PostgreSQL。",
      "source_type": "manual",
      "supersedes_evidence_ids": ["<owned-current-evidence-uuid>"],
      "idempotency_key": "deployment-target-correction-20260729"
    }
  ]
}
```

替换证据被接收入库时，目标会在同一原子动作中退役；即使后续 placement 被拒绝或
隔离，这个更正决定仍会保留。这样不会让过期证据继续保持有效。

若无需替代证据，请使用 `retract_evidence`，提供自己拥有的当前 ID、有界原因和
幂等键：

```json
{
  "evidence_ids": ["<owned-current-evidence-uuid>"],
  "reason": "来源已撤回。",
  "idempotency_key": "withdrawn-source-20260729"
}
```

这两种操作都会追加生命周期事件，绝不会物理删除证据或追踪链路。当前检索会排除
已退役证据；事件发生前的历史 `known_at` 视图仍可以显示当时系统知道的内容。

`correct_relationship` 用于替换调用 profile 自己拥有的一条活跃 Relationship，
不会重写或删除原记录。调用者必须提供当前版本、精确的有效证据 span、有界原因，
以及需要更正的端点或 predicate：

```json
{
  "action": "submit",
  "relationship_id": "<自己拥有的活跃-relationship-uuid>",
  "expected_version": 1,
  "patch": {
    "object_entity": {
      "entity_id": "<正确的同团队-entity-uuid>"
    }
  },
  "supports": [
    { "evidence_id": "<支持证据-uuid>", "start": 0, "end": 38 }
  ],
  "reason": "对象被解析成了错误的 Entity。",
  "idempotency_key": "relationship-correction-20260808"
}
```

接受后，Dense-Mem 会原子地 supersede 原 Relationship、创建或复用活跃后继、复制
有效支持链路，并追加 correction event 与 `corrects` cross-reference。同团队的其他
profile 可以读取团队可见记忆，但不能更正作者的 Relationship。若 Entity 名称存在
歧义，只允许作者确认一次；确认成功前原 Relationship 保持活跃。

## 检索与图状态

`recall_memory` 是证据优先但受支持路径约束的接口。只有最终 hydration 证明存在
活跃、与查询相关且在请求 `valid_at` 与 `known_at` 下仍有效的 Relationship 支持路径时，
`results[]` 才包含证据上下文。相关 Relationship、community 和 Hypothesis 是独立的
有界字段；候选项和 Hypothesis 不会作为默认记忆结果返回。

```text
remember 证据（可选 Entity/Relationship 提议）
        |
        v
持久化暂存 -> 校验后的 placement -> 活跃且有效的 Relationships
        |                                      |
        +-- 生命周期事件 -----------------------+
                                               |
                                               v
                          按支持路径约束的证据检索与追踪链路
```

## MCP 工具目录

当前契约版本为 `dense-mem.v2.4`。用 MCP `tools/list` 发现已授权目录；服务端对
`tools/call` 施加相同的 scope、功能和可见性检查。

| 工具 | 使用方 | 注册方式 | 用途与能力 |
|------|--------|----------|------------|
| `remember` | 两者 | 正式与评估镜像 | 正式证据写入；评估 harness 也通过真实 intake 导入 corpus。 |
| `get_submission_status` | 两者 | 正式与评估镜像 | 查询 `remember` 与 `correct_relationship` 的所有者隔离状态；harness 用它等待导入 placement。 |
| `retract_evidence` | 正式 | 正式与评估镜像 | 退役调用者拥有的证据，同时保留只追加来源。 |
| `correct_relationship` | 正式 | 正式与评估镜像 | 仅作者可替换活跃且有支持的 Relationship；supersede 原记录并保留支持链路。 |
| `recall_memory` | 正式 | 正式与评估镜像 | 检索活跃证据上下文和 Relationship；有可执行后续动作时返回 `suggested_actions`。 |
| `trace_memory` | 正式 | 正式与评估镜像 | 沿证据、决策和生命周期追踪一条同团队 Relationship。 |
| `submit_recall_session_feedback` | 正式 | 两种镜像中均为条件注册 | 记录有界的 session 级检索质量反馈；只有 recall feedback 启用时才注册。 |
| `list_dreams` | 正式 | 两种镜像中均为条件注册 | 列出可审查 Hypothesis；仅在 authenticated team 的 Dreaming 生效时注册。 |
| `get_dream` | 正式 | 两种镜像中均为条件注册 | 在同一团队 Dreaming gate 下获取 Hypothesis 与来源引用。 |
| `resolve_dream_feedback` | 正式 | 两种镜像中均为条件注册 | 用独立证据确认或否定 Hypothesis；不确定项保持 unresolved。使用同一团队 Dreaming gate。 |
| `export_memory_pack` | 正式 | 正式与评估镜像 | 导出选定活跃 Relationships 及其支持来源。 |
| `eval_list_knowledge_refs` | 评估 harness | 仅评估镜像 | 分页列出稳定、团队隔离的知识引用，用于 seed 文档映射。 |
| `eval_run_dream_cycle` | 评估 harness | 仅评估镜像 | 为评估运行隔离、有界的手动 Dream cycle，并可传入 seed Hypothesis。 |
| `eval_run_recall_case` | 评估 harness | 仅评估镜像 | 执行当前 recall 逻辑并返回用于确定性评分的 ranked/context refs。 |

正式发布 binary 不带 `evaluation` build tag，因此任何环境变量或控制面设置都无法在
线上注册评估工具。评估 target 只增加上表三个 harness 工具。
`eval_get_manifest`、`eval_get_knowledge_item`、
`eval_list_recall_feedback_events`、`eval_get_recall_feedback_event` 和
`eval_score_retrieval_case` 已删除，因为当前 harness 不使用它们。

启用 recall feedback 且 feedback snapshot 保存成功后，
`recall_memory.suggested_actions` 会携带对应 recall ID，提示调用
`submit_recall_session_feedback`。团队有效 Dreaming 启用且 recall 返回 Hypothesis 时，
还会提示调用 `resolve_dream_feedback`：只有独立证据充分时才确认 true 或 false；
不确定项保持 unresolved。

本地评估使用已提交的 compose 示例。它会构建评估 target，并默认读取仓库根目录中
未提交的 `.env`：

```bash
docker compose -p densemem_eval \
  -f examples/docker-compose.evaluation.yml up -d --build

go run ./cmd/eval-seedgen \
  --preset local_eval_100 \
  --out tests/eval/seeds/local_eval_100 \
  --suite tests/eval/suites/local_eval_100.jsonl
```

`local_eval_100_v2` 包含 100 条 corpus 与 25 个评分 case。它只用于检查评估镜像和
harness plumbing，不能替代已批准的确定性 1k release gate。此 smoke 建议使用
`IMPORT_CONCURRENCY=5`；完整评估仍可在 harness 的上限 10 内配置。

memory-pack 仅导出当前的 `dense-mem.memory-pack.v2.4` artifact。导入和候选发现
流程不属于公共契约。

## 支持的 HTTP 表面

| 表面 | 路径 | 用途 |
|------|------|------|
| Streamable HTTP MCP | `GET /mcp`、`POST /mcp` | 支持的外部记忆集成契约。 |
| 用户门户 | `/ui` 与 `/ui/api/*` | 第一方浏览器界面。 |
| 控制门户 | `/control/api/*` | 私有或独立管理入口。 |
| 健康检查 | `/health`、`/ready` | 容器存活和就绪检查。 |

没有受支持的公共 REST memory API。不要自动化浏览器路由，也不要依赖已退役的
`/api/v1` 路径。

## Telemetry Overlay

Prometheus telemetry 默认关闭。若要为第一方 dashboard 收集 HTTP、embedding、
verifier、recall、feedback 和 conflict-review 指标，请和基础 stack 一起启动 overlay：

```bash
curl -fsSLo prometheus.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/prometheus.yml
curl -fsSLo docker-compose.telemetry.yml \
  https://raw.githubusercontent.com/markhuangai/dense-mem/main/examples/docker-compose.telemetry.yml

export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml up -d
```

overlay 会在 `127.0.0.1:9090` 启动 Prometheus，并通过
`TELEMETRY_PROMETHEUS_JOB=dense-mem` 限定 dashboard 查询。自由文本 recall-feedback
comment 保存在有界调查记录中；Prometheus 只接收有界 labels。

## 职责边界

| 范围 | Dense-Mem 负责 | 宿主 LLM 负责 |
|------|----------------|---------------|
| 证据 | 精确暂存、来源、生命周期和所有者检查 | 决定提交哪些来源材料 |
| 语义状态 | 校验、确定性策略和支持有效性 | 提出可选 Entity/Relationship 提议 |
| 检索 | 活跃证据上下文和 Relationship 句柄 | 决定对话中引用或追问什么 |
| 更正 | 已授权的 supersession、retraction 和只追加链路 | 判断是否应当更正 |
| 运维 | teams、profiles、API keys、审计和门户 | MCP 客户端配置 |

## 数据出站与一致性

Dense-Mem 可以向配置的 embedding 与 verifier provider 发送证据文本、提议上下文和
检索查询。自托管 provider 会把这些流量留在你的边界内；托管 provider 不会。Embedding
是派生且带版本的状态，不能覆盖更新的来源。启动检查会阻止混用不兼容的 embedding
模型或维度。

## 文档

| 目标 | Wiki 页面 |
|------|-----------|
| 本地运行 Dense-Mem | [Quick Start](https://github.com/markhuangai/dense-mem/wiki/Quick-Start) |
| 使用证据生命周期和检索 | [Using Dense-Mem](https://github.com/markhuangai/dense-mem/wiki/Using-Dense-Mem) |
| 配置 provider、Redis 与入口 | [Configuration](https://github.com/markhuangai/dense-mem/wiki/Configuration) |
| 了解系统设计 | [Architecture](https://github.com/markhuangai/dense-mem/wiki/Architecture) |
| 查看 MCP 与门户路由 | [Technical Reference](https://github.com/markhuangai/dense-mem/wiki/Technical-Reference) |

## License

Apache-2.0
