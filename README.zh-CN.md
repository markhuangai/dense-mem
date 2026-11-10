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
证据，再通过经过校验的服务端策略派生语义状态，并返回仍然有效的 Evidence、来源和主题线索。PostgreSQL 是知识、生命周期、来源、搜索、授权和审计的唯一持久
权威；Redis 只用于协调。单节点部署可使用进程内协调；多实例部署需要 Redis 或等效的
分布式协调实现。

宿主 LLM 负责对话和判断。Dense-Mem 负责证据持久化、所有者授权、生命周期事件、
Evidence 有效性和有界检索。面向外部自动化的记忆契约只有 `/mcp`；浏览器路由是第一方
产品界面，不是另一套公共自动化 API。

Dense-Mem 也是这篇研究预印本的一部分：
[Governed Enterprise AI Memory Beyond RAG: From Vector Retrieval to Permissioned
Knowledge Graphs](https://zenodo.org/records/21403316)。

## 在线体验

打开 [https://demo-dense-mem.markhuang.ai](https://demo-dense-mem.markhuang.ai)，
创建临时隔离团队后可用一次性测试数据体验核心流程。

<p align="center">
  <img src="assets/readme-hero.jpg" alt="AI 客户端提交证据，受治理的记忆服务记录生命周期并返回带来源的 Evidence。" />
</p>

## 为什么使用 Dense-Mem

- 证据精确、可持久保存且只追加。生命周期动作只改变有效状态，不删除来源或追踪链路。
- 精确 Evidence、occurrence 和 source revision 保留原始来源；更替和撤回只追加生命周期事件。
- Provider 输出只是提议。闭合 schema 校验和确定性服务端策略决定持久状态。
- Recall 直接检索有效 Evidence。主题只组织共享 Evidence，不建立新的事实支持。
- 团队可见性与 owner 修改权限不同。共享读取不会授予修改其他作者 Evidence 的权限。

认证会解析出不可变的调用者上下文：`team + identity + membership + 永久 owner alias +
可选 credential`。SSO 浏览器 session 使用所选 membership 的永久 owner alias，且没有
直接 credential。API key 请求会携带 credential；其稳定 ID 同时也是永久 owner alias。
客户端不能用 team、identity、membership 或 credential 字段选择或替换语义 owner。

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

基础 stack 使用带 pgvector 的 PostgreSQL 作为唯一持久权威。v2.6.2 发布要求先由
停服迁移写入兼容 cutover marker；运行时不再包含旧数据库或回退路径。默认本机端口：

```text
MCP:            http://127.0.0.1:8080/mcp
用户门户:       http://127.0.0.1:8080/ui
控制门户:       http://127.0.0.1:8090/
```

使用 `CONTROL_PORTAL_TOKEN` 打开控制门户，从 **All teams** 创建团队，再进入 **Credentials** 创建第一个 API key。
侧边栏按团队、运维和管理分组，团队选择器用于切换工作区。用户门户提供 **Search memory**、**Ontology** 以及当前权限允许的管理页面；**Connect** 显示团队 ID、MCP 地址和内存访问权限。两个门户均支持跟随系统、浅色和深色主题，导航可折叠，页面链接支持刷新和浏览器前进/后退。
控制面自动化使用同一个私有 API：

```bash
control_token="<.env 中的 CONTROL_PORTAL_TOKEN>"

curl -fsS -X POST http://127.0.0.1:8090/control/api/teams \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"primary-memory"}'

curl -fsS -X POST http://127.0.0.1:8090/control/api/teams/<team-id>/credentials \
  -H "Authorization: Bearer ${control_token}" \
  -H "Content-Type: application/json" \
  -d '{"name":"default credential"}'
```

正式镜像只包含一个项目可执行文件 `/app/server`。它会在开始提供服务前，借助数据库
session lock 自动执行待处理的 PostgreSQL migration，因此 Compose 不需要单独的
migration 容器。共享同一个可写 primary 的多个 server replica 会串行执行该启动步骤。
普通滚动发布期间，migration 必须与上一版应用保持向后兼容。v2.6.1 同步 Remember
迁移仍是停服边界：必须停止所有 server 实例、完成所需的 PostgreSQL 快照，并在启动新二进制
前应用停服边界；v2.6.2 的 evidence-first 激活在该边界之后进行。彼此独立的数据库仍需分别由连接到该数据库的 server 完成 migration。
管理操作通过私有控制门户/API 完成；ontology maintenance 和自动 conflict review 由 server 后台
worker 执行。

migration 默认没有总体超时限制（`POSTGRES_MIGRATION_TIMEOUT_SECONDS=0`）；可设置
不超过 86400 的正整数秒数来限制单次执行时间。进程关闭仍会取消迁移，每 10 秒记录进度。
迁移完成前 server 不会进入就绪状态。镜像 healthcheck 保留 30 分钟启动宽限期，并在首次
检查成功后进入正常检测。大型迁移应延长部署的启动、healthcheck 宽限期和 rollout deadline，
避免编排器在迁移尚未完成时重启 server 或放弃部署。

RC 标签为 `vX.Y.Z-rc.N` 和 `demo-vX.Y.Z-rc.N`。稳定版标签为 `vX.Y.Z`、
`latest` 和 `demo-vX.Y.Z`；不提供滚动 demo 标签。

启动时必须配置完整的 embedding 和 verifier：`AI_API_URL`、
`AI_API_KEY`、`AI_API_EMBEDDING_MODEL`、`AI_API_EMBEDDING_DIMENSIONS`、
`AI_VERIFIER_MODEL`。compose 示例为 embedding 提供
OpenAI 默认值；chat model 需要在 `.env` 中明确选择。

`AI_REMEMBER_MODEL` 和 `AI_CONFLICT_REVIEW_MODEL` 是对应现有 AI
session 的可选覆盖。未设置或仅包含空白字符时使用 `AI_VERIFIER_MODEL`；已配置
model 的失败会直接返回，不会改用 fallback model。

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
模型在加载期间可能超过默认 60 秒超时。Remember 会在请求 deadline 内同步运行；客户端
在超时或临时 provider 故障后应使用相同幂等键重试。

## 证据生命周期

公共契约为 `dense-mem.v3.0.0`。`remember` 接收精确 Evidence 和来源，整批必须显式选择
`target_space: "private"` 或 `"team_shared"`。认证决定实际团队、owner 和空间 ID。
每条 Evidence 最多 999 个 Unicode code point。已退役的图提议字段会在 provider 调用前拒绝。

```json
{
  "target_space": "team_shared",
  "idempotency_key": "deployment-source-20261110",
  "evidence": [{
    "content": "Dense-Mem stores durable memory in PostgreSQL.",
    "source_type": "manual",
    "source": "Deployment decision"
  }]
}
```

服务端先持久化原始 Evidence 与处理意图，在事务外完成安全、等价性和 Evidence 冲突评估，
再完成必需的 embedding。校验整份响应后，Evidence、occurrence、source revision、生命周期、
search document 和终态 receipt 原子提交。必需 provider 失败或取消不会部分接受整批。

调用前保留完整请求和 operation key。未知结果用完全相同的请求与 key 重放；同 key 改变请求会冲突。
旧 Remember key 保留原始身份、hash 和结果，返回不可重试的 `retired_contract` 指引。

替换自己的当前 Evidence 时，在新条目中传 `supersedes_evidence_ids`；也可使用
`previous_source_revision` 推进来源版本，两者不能混用。成功接受替换时才会退役旧条目。
`retract_evidence` 接收自己的 ID、有限原因和 idempotency key。两种操作均保留精确内容和追踪链路。

功能开关控制的 `ingest_session` 接收精确用户事件并写入认证私有空间。
`session_evidence_excerpts_v2` 只选择当前事件中的原文摘录，保留原始 Unicode offset。
先前上下文只读；空提取显式返回 `memory_outcome: "no_memory"`。事件身份、重放、取消与 generation fence 仍生效。

## Recall、主题、Trace 与导出

`recall_memory` 直接返回有效 Evidence、精确有界上下文、等价 ID、Evidence 冲突、search state
与显式 degradation。`known_evidence_ids` 排除已知有效结果。

`related_topics` 包含 `topic_id`、名称、描述、有效未见 Evidence 数量和 rank；
`topics_truncated` 显式表示省略。`topic_limit` 默认 3，范围 0–10。
后续用 1–10 个唯一 UUID 的 `topic_ids` 选择当前直接 membership 的并集，不扩展子主题。
计数与选择前应用授权、生命周期、时间、freshness 和已知 Evidence 排除。
缺失、外团队或陈旧主题统一返回有界 `topic_unavailable`；普通 Recall 的可选主题故障显式降级。
时间检索仍使用当前主题元数据。

Ontology 只组织共享 Evidence；私有记忆不参与。主题、等价分组、manager override、
enable/pause/disable 与 generation fence 保留。暂停保留有效发布，禁用后 Recall 不使用组织信息。
主题 parent 链只表示组织层级，不建立事实支持。

`trace_memory` 接收 `evidence_id`，返回 Evidence 决策、occurrence、source revision 与生命周期。
保留内容和 transition 开关；默认最多 100 个事件与 2,000 rune，硬上限 500 与 8,000。
`export_memory_pack` 接收名称和 1–500 个 Evidence ID，生成
`dense-mem.memory-pack.v3.0`，保留来源、canonical hash、边界和显式 omission。

## MCP 工具目录

用 `tools/list` 发现闭合 schema；`tools/call` 使用相同 registry 与授权检查。

| 工具 | 用途 |
| --- | --- |
| `remember` | 精确 Evidence 和来源，显式选择 private/team_shared。 |
| `ingest_session` | 功能开关控制的私有原文摘录。 |
| `retract_evidence` | Owner 授权的 Evidence 撤回。 |
| `recall_memory` | 有效 Evidence、主题线索及直接主题后续检索。 |
| `trace_memory` | Evidence 决策、来源与生命周期。 |
| `submit_recall_session_feedback` | 启用时接收可选的有界反馈。 |
| `export_memory_pack` | 有界 Evidence 和来源导出。 |

评估 build 另有 `eval_list_knowledge_refs` 与 `eval_run_recall_case`。
正式 build 不能通过配置注册评估工具。事实图、Dream 与 community 契约从两个 profile 中退役。
Python core、ADK 和 LangChain 使用相同 v3 schema，示例见 [Python README](packages/python/README.md)。

升级前停止不兼容实例并验证匹配备份可恢复。普通启动在 workers/listeners 前执行有序 migration，
保留 Evidence fingerprint 与 scoped operation identity，删除图专用数据并清理 mixed payload。
Legal hold 或未知依赖会阻止迁移；迁移不调用 provider。提交前失败回滚；破坏性提交后必须恢复匹配备份，
有新写入后默认向前恢复。不可在迁移后的数据库上重启旧 binary。

生产日志默认 TRACE。DEBUG 保留受 credential protection 保护的完整评估 exchange、已接受原文、
主题选择原因与 Trace/export 边界。WARN 标记主题降级与 retired contract；INFO 记录 session resume
和迁移里程碑；ERROR 记录迁移阻塞/失败。Teardown 前收集完整 stdout 窗口，校验 2,048-byte chunk、
hash 和数量。保留 16 MiB provider body、64 MiB session envelope 与七天诊断保留边界。
不完整、截断或中断 capture 必须显式报告。

## 支持的 HTTP 表面

| 表面 | 路径 | 用途 |
|------|------|------|
| Streamable HTTP MCP | `GET /mcp`、`POST /mcp` | 支持的外部记忆集成契约。 |
| 用户门户 | `/ui` 与 `/ui/api/*` | 第一方浏览器界面。 |
| 控制门户 | `/control/api/*` | 私有或独立管理入口。 |
| 健康检查 | `/health`、`/ready` | 容器存活和就绪检查。 |

没有受支持的公共 REST memory API。不要自动化浏览器路由，也不要依赖已退役的
`/api/v1` 路径。

## Telemetry And Grafana

Prometheus telemetry 默认关闭。若要收集 HTTP、embedding、verifier、assessor、
recall feedback、Remember、cost 和 Evidence 生命周期指标，请和基础
stack 一起启动 telemetry overlay。Grafana operator dashboard 是可选的：

```bash
export TELEMETRY_SCRAPE_TOKEN="$(openssl rand -hex 32)"
export GRAFANA_ADMIN_PASSWORD="$(openssl rand -hex 32)"
docker compose \
  -f examples/docker-compose.base.yml \
  -f examples/docker-compose.telemetry.yml \
  -f examples/docker-compose.grafana.yml \
  up -d
```

Prometheus 和 Grafana 分别在 `127.0.0.1:9090` 和 `127.0.0.1:3000` 提供服务；两个端口
都只绑定 loopback。Grafana 使用 `GRAFANA_ADMIN_PASSWORD`，并从
`examples/grafana/provisioning` 自动加载已检查的 dashboards。Datasource ID 由安装生成。

Grafana 提供 HTTP、AI、recall、Remember 和 Evidence lifecycle measures。顶部时间选择器
控制图表范围；`Rolling totals` 选择器控制 counter 窗口和 canonical ledger gauge 窗口。
缺少 provider usage、pricing 或 recall feedback 会显示为无数据，而不是伪造零值。
ledger collector 失败时会抑制对应 gauges 并显示 collection 状态。Replica 会使用
`max` 聚合 durable gauges；counter measures 会跨实例聚合，并保留 sparse first-sample 语义；
range rates 使用 Grafana query step。已有 scoped telemetry metrics 保留供第一方 API 使用的
team/profile labels；Grafana 不用这些 labels 筛选或分组，而是聚合为 system-wide 数值。
新增 operational metrics 不加入 identity labels，metric labels 也不包含 evidence 或
request text。Lifecycle dashboard 是 system-wide，因为 canonical collector 没有 team 或
credential 维度。

控制端的 Metrics 和用户端的 Usage dashboard 已在真实 Prometheus 数据与 Grafana
13.2.2 完成逐项 parity 验证后移除。Team overview 请求汇总、私有诊断与设置仍保留。Provisioning 和 series 对照见
[`examples/grafana/README.md`](examples/grafana/README.md)。

## 职责边界

| 范围 | Dense-Mem 负责 | 宿主 LLM 负责 |
|------|----------------|---------------|
| 证据 | 精确暂存、来源、生命周期和所有者检查 | 决定提交哪些来源材料 |
| 评估 | 完整校验、确定性策略与原子接受 | 选择精确 Evidence 和来源 |
| 检索 | 有效 Evidence 和主题线索 | 决定对话中引用或追问什么 |
| 更正 | 已授权的 supersession、retraction 和只追加链路 | 判断是否应当更正 |
| 运维 | teams、memberships、credentials、API keys、审计和门户 | MCP 客户端配置 |

## 数据出站与一致性

Dense-Mem 可以向配置的 embedding 与 verifier provider 发送 Evidence 文本、评估上下文和
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
