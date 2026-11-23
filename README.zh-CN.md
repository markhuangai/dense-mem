<h1 align="center">Dense-Mem</h1>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Dense--Mem-governed_AI_memory-0f766e?style=for-the-badge&logo=github&logoColor=white" alt="Dense-Mem" />
</p>

<p align="center">
  <strong>面向 MCP 的自托管记忆服务：保留精确 Evidence、来源与受治理的检索。</strong>
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

<p align="center">
  <a href="https://markhuangai.github.io/dense-mem/">交互式文档</a> ·
  <a href="https://markhuangai.github.io/dense-mem/Quick-Start/">部署指南</a> ·
  <a href="https://markhuangai.github.io/dense-mem/Architecture/">工作原理</a>
</p>

Dense-Mem 是使用 Streamable HTTP 的独立 HTTP MCP 记忆服务器。AI 客户端决定
记住什么；Dense-Mem 保存精确的 Evidence、来源和生命周期，并在不同对话中检索
当前调用者有权访问的记忆。

PostgreSQL 与 pgvector 是唯一持久权威。Redis 只负责协调：单节点部署可以使用
进程内协调；多实例部署需要 Redis 或等效的分布式协调实现。

## 在线体验

[打开在线演示](https://demo-dense-mem.markhuang.ai)，创建临时隔离团队，
用一次性测试数据体验记忆流程，再决定如何自行部署。

<p align="center">
  <img src="assets/readme-hero.jpg" alt="AI 客户端提交 Evidence；Dense-Mem 保留来源和生命周期，并返回有权访问的记忆。" />
</p>

## 为什么使用 Dense-Mem

- **保留原文。** 精确 Evidence 保留 occurrence、source revision、来源和只追加的
  生命周期历史。
- **明确访问权限。** 私有记忆与团队共享记忆有显式目标；共享可见性不会授予修改
  其他 owner 的 Evidence 的权限。
- **追踪历史。** Recall 返回有效 Evidence，Trace 解释来源；更正和撤回保留历史链路。
- **组织而不制造事实。** 派生主题帮助发现内容，Evidence 始终是规范记忆。

[Using Dense-Mem](https://markhuangai.github.io/dense-mem/Using-Dense-Mem/)
介绍记忆使用流程；[Architecture](https://markhuangai.github.io/dense-mem/Architecture/)
解释系统设计。

## 快速开始

先安装 Docker 与 Compose。以下全新部署示例将镜像和配置文件固定到同一正式版本：

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

在 `.env` 中设置：

| 配置项 | 用途 |
| --- | --- |
| `POSTGRES_PASSWORD` | 本地 PostgreSQL 的密码 |
| `CONTROL_PORTAL_TOKEN` | 私有控制门户的访问令牌 |
| `AI_API_KEY` | 模型 provider 的凭据 |
| `AI_VERIFIER_MODEL` | 用于评估 Evidence 的聊天模型 |

示例提供 OpenAI embedding 默认配置。使用其他 provider 时，应一起调整 endpoint、
模型和维度；详见 [Configuration](https://markhuangai.github.io/dense-mem/Configuration/)。

```bash
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8080/ready
```

启动过程先执行 migration，再进入就绪状态。后续启动仍应保留镜像版本；修改已有
数据库前，请阅读 [Operations Runbook](https://markhuangai.github.io/dense-mem/Operations-Runbook/)。

## 连接 AI 客户端

1. 使用 `CONTROL_PORTAL_TOKEN` 打开私有控制门户 `http://127.0.0.1:8090/`，
   创建团队，再创建该团队的第一个 credential。
2. 打开用户门户 `http://127.0.0.1:8080/ui`。**Connect** 页面提供 MCP 连接信息；
   本地 MCP endpoint 是 `http://127.0.0.1:8080/mcp`。
3. 按照 [MCP Clients](https://markhuangai.github.io/dense-mem/MCP-Clients/)
   配置宿主客户端，再尝试 Remember、Recall 和 Trace 流程。

控制监听端口应保持私有。配置的托管模型 provider 可能接收 Evidence 和查询；
如需让这些流量留在自己的基础设施内，请使用自行托管的 provider。外部记忆自动化
统一使用 `/mcp`。

## 文档导航

| 目标 | 指南 |
| --- | --- |
| 了解记忆工作原理 | [记忆流程演示](https://markhuangai.github.io/dense-mem/) · [Using Dense-Mem](https://markhuangai.github.io/dense-mem/Using-Dense-Mem/) |
| 配置与运维 | [Configuration](https://markhuangai.github.io/dense-mem/Configuration/) · [Operations](https://markhuangai.github.io/dense-mem/Operations-Runbook/) |
| 选择部署示例 | [Examples](examples/README.md) · [Grafana](examples/grafana/README.md) |
| 集成客户端 | [Python / ADK / LangChain](packages/python/README.md) · [stdio MCP proxy](packages/mcp-proxy/README.md) |
| 开发与测试 | [Scripts](scripts/README.md) · [Evaluation](tests/eval/README.md) · [ADRs](adr/) |

文档站提供交互式流程演示和详细参考。与仓库文件配套的操作说明放在对应目录的 README。
行为变更应关联配套的 `gh-pages` PR，或说明文档站为何无需更新。

Dense-Mem 也是这篇研究预印本的一部分：
[Governed Enterprise AI Memory Beyond RAG: From Vector Retrieval to Permissioned
Knowledge Graphs](https://zenodo.org/records/21403316)。

## License

[Apache-2.0](LICENSE)
