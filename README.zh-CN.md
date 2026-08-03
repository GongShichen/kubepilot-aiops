# KubePilot-AIOps

[English](README.md) | [简体中文](README.zh-CN.md)

KubePilot 是一个 Eino-native Kubernetes AIOps Agent 系统。类型安全的 Eino Graph 负责确定性的 Incident 生命周期，三个 Eino ADK Agent 负责复杂决策，Eino ToolsNode 承载全部外部能力，Eino Interrupt 负责人工审批和可恢复执行。

控制平面使用 Go、Gin 和 Eino 实现。系统接收 Alertmanager 事件、关联告警、收集指标/日志/Trace/Kubernetes 证据，执行基于证据的根因诊断，生成受约束的恢复方案，在人工审批后通过 `client-go` 执行动作并验证结果。Prometheus、Loki、Jaeger、Milvus、PostgreSQL、Redis 和官方 Drain3 解析器为 Agent 提供证据与状态基础设施。

## 架构

```text
Minikube：gateway → order → payment → MySQL/Redis
    │          指标 / 日志 / OTLP Trace
    ▼
Docker Compose：Agent + Log Indexer + PostgreSQL + Redis + Prometheus
                + Loki + Jaeger + Grafana + Milvus + Drain3 Sidecar
```

Drain3 不在 Incident 查询关键路径中。独立 Go Log Indexer 持续消费 Loki，通过版本化、幂等的 WebSocket 批量调用 Drain3，使用用户配置的 BGE endpoint 生成向量，并将模板索引写入 PostgreSQL 和 Milvus。查询阶段只读取 Loki 与预构建索引；索引不新鲜时回退 Loki。

## Agent 架构

```mermaid
flowchart TD
    A["Incident Intake"] --> B["Alert Correlator"]
    B --> S["Supervisor Agent<br/>Evidence Plan"]
    S --> T["并行 Evidence ToolsNode<br/>Prometheus · Loki · Jaeger · Kubernetes"]
    T --> F["Evidence Fusion"]
    F --> H["Historical Retrieval Tool"]
    H --> D["Diagnosis Agent<br/>Hypothesis Verification"]
    D -->|"最多补采一次"| T
    D -->|"confidence < 0.80"| N["NEEDS_ATTENTION"]
    D --> R["Recovery Agent<br/>只生成 Proposal"]
    R --> V["Proposal Validation + Kubernetes DryRunAll"]
    V --> I["Eino StatefulInterrupt<br/>人工审批"]
    I -->|"ResumeWithData"| X["Action ToolsNode"]
    X --> Q["Verification ToolsNode<br/>连续三次健康"]
```

- **Eino 是运行时，而不是包装层：** `compose.Graph` 是工作流运行时，`adk.NewChatModelAgent` 是 Agent 运行时，`compose.NewToolNode` 是能力运行时，`StatefulInterrupt`/`ResumeWithData` 是审批运行时。
- **只有三个核心 Agent：** Supervisor 规划有界 Evidence，Diagnosis 执行基于证据的假设验证，Recovery 只创建安全 Proposal。采集、校验、执行和验证属于确定性 Workflow/Tool。
- **类型安全且可恢复：** 版本化 WorkflowState、Evidence Schema、Dry-run 结果和服务端生成的 ExecutionContext 存入 Redis checkpoint；PostgreSQL 保存可审计的长期业务状态。
- **基于证据的推理：** Diagnosis Agent 最多生成三个假设，为每个假设附带支持 Evidence ID 和可证伪条件；置信度低于 `0.80` 时进入 `NEEDS_ATTENTION`，不会强制输出结论。
- **统一 Capability Registry：** 所有 Agent 可见的读取和修改能力都是 Eino Tool，具备 JSON Schema、节点 allowlist、timeout、输入/输出大小限制和 Action 审批中间件。Tool 不接受原始 SQL、PromQL、LogQL、kubectl、Shell、Milvus filter 或任意 Kubernetes manifest。
- **官方流式模型组件：** OpenAI-compatible 和 Anthropic-compatible 均使用锁定版本的 Eino 扩展。Eino 负责合并流式 Tool Call 分片；URL、API Path、API Key 和模型名完全由用户配置。
- **能力门控：** 启用恢复能力前，Agent 会执行无副作用的 Tool Calling 探测。无法生成指定结构化工具调用的 endpoint 不会进入真实恢复模式。
- **预索引 Hybrid Log：** 查询 Tool 组合 Loki metadata/keyword 与持续维护的 Drain3/BGE/Milvus 索引，不会等待同步日志解析。
- **Human-in-the-loop 恢复：** Recovery 只能提出 `restart_pod`、`scale_deployment` 或 `rollback_deployment`。Kubernetes `DryRunAll` 成功后才能审批；执行还必须校验服务端 ExecutionContext、幂等键、namespace allowlist、mutation hash、UID 和 resourceVersion。
- **闭环验证：** 动作完成后，确定性的 Verification ToolsNode 连续采样工作负载健康状态，并将最终 Incident、Proposal、审批、审计和验证状态持久化到 PostgreSQL。
- **Agent 可观测性：** Eino Callback 将 Graph/Agent/Node/Tool 生命周期写入 OpenTelemetry、Prometheus、PostgreSQL 审计与 tool_calls 表以及 Incident SSE，同时不记录隐藏推理、凭据或未裁剪原始日志。
- **可消融评测：** 同一个 Runner 可以评测 `llm-only`、`vector-rag` 和完整 `kubepilot` 路径，从而量化历史检索与多源 Agent 带来的贡献。

## 环境要求

- macOS 或 Linux，至少 16 GB 内存；建议为容器提供 24 GB 可用内存。
- Go 1.26.2。
- Docker/Colima、Minikube、kubectl 和 Helm。
- 一个支持 Tool Calling 的 OpenAI-compatible 或 Anthropic-compatible 对话模型 endpoint。
- 一个提供 OpenAI-compatible Embeddings API 的 Embedding 模型 endpoint。

请根据宿主机可用资源配置容器运行时和 Minikube。执行完整 Benchmark 时，需要为 PostgreSQL、观测组件、Milvus 和 Kubernetes 工作负载预留足够内存。

## 配置

```bash
cp .env.example .env
```

必填配置：

```env
API_TOKEN=...
ALERTMANAGER_WEBHOOK_TOKEN=...
CHAT_PROTOCOL=openai-compatible       # 或 anthropic-compatible
CHAT_BASE_URL=https://...
CHAT_API_PATH=/chat/completions       # Anthropic 使用 /v1/messages
CHAT_API_KEY=...
CHAT_MODEL=...
CHAT_MAX_RETRIES=1                    # 临时传输错误、429、5xx 最多重试一次
CHAT_REASONING_EFFORT=low             # reasoning 模型可选
CONFIG_RELOAD_INTERVAL=2s             # 无需重启 Agent，轮询挂载的 .env
CONFIG_RELOAD_RETRY_INTERVAL=30s      # 候选模型探测失败后的重试间隔
EMBEDDING_BASE_URL=https://...
EMBEDDING_API_PATH=/embeddings
EMBEDDING_API_KEY=...
EMBEDDING_MODEL=your-embedding-model
EMBEDDING_DIMENSIONS=1024
EMBEDDING_BATCH_SIZE=10               # provider 限制批量输入时可调低
EMBEDDING_REQUEST_INTERVAL=1s         # provider 限流节奏；429/5xx 会重试
DRAIN3_TOKEN=...
BUSINESS_PROBE_URL=...                # 可选的端到端恢复探针
```

密钥只从环境变量或未纳入版本控制的 `.env` 读取。健康检查响应、日志、Trace 和 Benchmark manifest 均不会记录密钥；Docker 构建上下文也会排除 `.env`、运行时凭据和 Benchmark 产物。

所有 Agent 模型节点都使用 Eino 流式接口。OpenAI-compatible SSE delta 和 Anthropic `input_json_delta` 分片由 Eino 合并后才会执行 Tool。

Agent 会监听只读挂载的 `.env` 中的对话模型配置变化。候选 Client 在后台完成校验和 Tool Calling 探测，成功后才原子替换当前 Client；探测失败会继续保留旧模型，HTTP 启动不会被模型探测阻塞。每个 Incident 在工作流开始时固定一份模型快照，避免诊断中途切换；热加载健康状态和日志不会暴露 API Key。

仓库中的 Alertmanager 开发配置使用 `change-alert-token`。首次本地运行时，应在 `.env` 中配置相同值，或同时修改 `deploy/docker/alertmanager/alertmanager.yml`。

## 本地部署

```bash
make bootstrap
make doctor
make up
```

`make up` 依次执行：

1. 启动 Colima 和启用 Calico 的 Minikube 集群。
2. 在 `.runtime/` 下生成容器可用的 kubeconfig 和 Prometheus 客户端凭据。
3. 启动 PostgreSQL、Redis、Drain3、Prometheus、Alertmanager、Loki、Grafana、Jaeger、Milvus 及其依赖。
4. 构建并加载 Go demo-service 镜像。
5. 部署 demo、benchmark namespace，以及 Alloy 和 kube-state-metrics。
6. 启动 Go Log Indexer 和 Go Agent。

本地服务入口：

| 组件 | URL |
|---|---|
| KubePilot | http://localhost:8080 |
| Grafana | http://localhost:3000 |
| Prometheus | http://localhost:9090 |
| Alertmanager | http://localhost:9093 |
| Loki | http://localhost:3100 |
| Jaeger | http://localhost:16686 |

使用 `make down` 停止服务并保留数据。`make destroy` 会在明确确认后删除 Minikube 集群和 Compose volumes。

## 十分钟演示

1. 打开 KubePilot，在本地控制台中输入 `API_TOKEN`。
2. 检查模型配置：

   ```bash
   curl -H "Authorization: Bearer $API_TOKEN" http://localhost:8080/api/v1/model/health
   curl -X POST -H "Authorization: Bearer $API_TOKEN" http://localhost:8080/api/v1/model/probe
   ```

3. 向 gateway 发送流量：

   ```bash
   kubectl -n kubepilot-demo port-forward service/gateway-service 18080:8080
   curl http://localhost:18080/checkout
   ```

4. 在 benchmark namespace 创建受控故障：

   ```bash
   make benchmark-smoke
   ```

5. 查看 Incident 证据和恢复方案，在控制台审批，并观察验证过程。
6. 在 Grafana 和 Jaeger 中查看关联的指标、日志和 Trace。

## API

版本化 API 规范位于 `api/openapi.yaml`。除 Alertmanager webhook 使用独立 token 外，所有 `/api/v1` 路由都要求 `Authorization: Bearer <API_TOKEN>`。恢复审批还必须提供 `Idempotency-Key`。

手动创建 Incident：

```bash
curl -X POST http://localhost:8080/api/v1/incidents \
  -H "Authorization: Bearer $API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"severity":"critical","service":"payment-service","namespace":"kubepilot-demo","resource":"payment-service","summary":"HTTP 500 increased"}'
```

## Benchmark

场景目录不允许包含任意 Shell 命令。`benchmark/incidents.yaml` 会确定性展开为 100 个强类型场景：

| 类别 | Case 数量 |
|---|---:|
| CPU | 20 |
| Memory leak/OOM | 20 |
| Database | 20 |
| Network | 20 |
| Deployment | 20 |

无需集群即可验证场景：

```bash
make benchmark-validate
```

运行档位：

```bash
make benchmark-smoke       # 5 个 case
make benchmark-standard    # 全部 100 个 case，串行且相互隔离
make benchmark-diagnosis-compare # LLM-only、Vector-RAG、完整 KubePilot 各运行 100 个 case
make benchmark-correlation # 通过真实 Agent webhook 发送 100 组 ground-truth 告警
make benchmark-retrieval   # 通过 Loki、Drain3、Embedding、Milvus 处理 500,000 条日志
make benchmark-full
```

`standard`、`correlation` 和 `retrieval` 会读取 `.env`。正式运行必须使用真实、支持 Tool Calling 的对话模型和真实 OpenAI-compatible Embedding endpoint；本地 mock 只用于合同测试，不得作为正式结果。报告会记录实际 Embedding 模型和维度，确保不同模型的结果可以区分。

Retrieval 在独立的 `kubepilot-retrieval` Compose project 中运行，拥有独立的 Loki（端口 3200）、Drain3（端口 8181）、Milvus（端口 19531）、etcd、MinIO、Docker network 和 volumes，不会写入 Diagnosis/Agent 的观测或历史数据。正式运行会重建该 project，Milvus collection 还会按 run ID 进一步隔离。

中断后可以恢复运行或重新生成报告：

```bash
go run ./cmd/benchmark resume --run-id <run-id>
go run ./cmd/benchmark report --run-id <run-id>
```

Kubernetes Injector 只能执行编译进 Runner 的动作类型，拒绝操作 `kubepilot-benchmark` 以外的 namespace。Runner 会快照受影响资源、串行执行 case，并在成功、失败、取消或超时后恢复健康基线；清理失败会立即停止后续 case。

每次运行会在 `artifacts/benchmark/<run-id>/` 下生成 manifest、case JSONL、summary JSON、CSV 表格和 Markdown 报告。Manifest 包含场景 hash、模型协议/名称、endpoint hash、seed 和版本，但不包含凭据。`artifacts/` 中的运行报告和日志不会提交到版本库。

指标包括严格根因准确率、category/service/resource 准确率、Evidence Recall、恢复决策准确率、工具成功率、诊断/恢复延迟、告警分组 Precision/Recall/F1、检索 Recall@K/MRR 和 P50/P95/P99。正式报告只使用实际测量结果。

Diagnosis 对比在相同 Kubernetes 故障场景上评估三种方法：`llm-only` 只接收 Incident metadata；`vector-rag` 接收 Incident metadata 和历史 Incident；`kubepilot` 收集 Metric、Log、Trace、Kubernetes 和历史证据。根因定位要求 category、21 种 canonical variant 之一、service 和 resource 完全匹配；严格准确率还要求 required-evidence recall 至少为 50%。报告包含分类 Precision/Recall/F1、混淆矩阵、Evidence Precision/Recall/Groundedness、Brier Score、ECE、高置信错误率和十档置信度校准。

Benchmark 完整性规则：场景 ground truth 只在诊断结束后提供给 Runner/Scorer。Incident 请求使用不透明的观测时间窗口和通用摘要；Agent 不会收到场景 ID、Injector 名称、期望证据、允许动作或期望标签。生产诊断代码禁止包含根据 Benchmark 错误追加的 per-case 决策规则。Injector-specific 代码只存在于 Benchmark 环境，用于创建和清理故障。历史检索只能从独立版本化的 `benchmark/history.yaml` 写入 `kubepilot_history_v2`；严禁使用 `benchmark/incidents.yaml` 生成 Agent 历史记忆。Seed manifest 会记录历史数据集 hash 和 collection 名称。

## 开发与验证

```bash
make test
make lint
```

直接执行：

```bash
go test -race ./...
go vet ./...
docker compose -f deploy/docker/docker-compose.yml config --quiet
kubectl kustomize deploy/kubernetes/overlays/demo >/dev/null
kubectl kustomize deploy/kubernetes/overlays/benchmark >/dev/null
```

## 排障

- `make doctor` 会报告缺失工具或未启动的 Docker backend。
- Agent 无法访问 Kubernetes 时，在 Minikube 重启后重新执行 `make runtime`；Kubernetes API 端口可能发生变化。
- Prometheus 没有 Minikube 数据时，检查 `deploy/docker/prometheus/generated/` 和 `minikube-proxy` target。
- Loki 没有数据时，检查 `kubectl logs -n kubepilot-system daemonset/alloy`，并确认 `host.minikube.internal:3100` 可访问。
- Drain3 不可用时，检查 `drain3` 和 `log-indexer` 服务。Incident 诊断会继续使用 Loki metadata，并将模板/向量索引标记为 stale；查询链路不会临时解析日志。
- Diagnosis 进入 `NEEDS_ATTENTION` 时，应检查证据错误和模型探测结果，不要降低置信度阈值。
- Benchmark 清理失败时，应先恢复 benchmark namespace 再继续。Runner 会拒绝在脏环境中执行后续 case。
