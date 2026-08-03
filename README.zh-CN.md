# KubePilot-AIOps

[English](README.md) | [简体中文](README.zh-CN.md)

KubePilot 是一个面向本地 Kubernetes 环境的事故诊断与恢复平台，采用 Go、Gin、Eino Graph、Prometheus、Loki、Jaeger、Milvus、PostgreSQL、Redis 和官方 Drain3 解析器 Sidecar 构建。

平台接收 Alertmanager 事件、关联告警、收集指标/日志/Trace/Kubernetes 证据，通过假设驱动的方式诊断根因，生成受约束的恢复方案，在人工审批后使用 `client-go` 执行动作并验证恢复结果。

## 架构

```text
Minikube：gateway → order → payment → MySQL/Redis
    │          指标 / 日志 / OTLP Trace
    ▼
Docker Compose：Agent + PostgreSQL + Redis + Prometheus + Loki + Jaeger
                + Grafana + Milvus + Drain3 WebSocket Sidecar
```

Go Log Agent 通过 `ws://drain3:8081/ws/v1/parse` 批量发送日志。请求具备版本号和幂等语义；Sidecar 会缓存最近十分钟的响应并持久化 Drain3 miner snapshot。

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
CHAT_MAX_RETRIES=1                    # timeout、429、5xx 最多重试一次
CHAT_REASONING_EFFORT=low             # reasoning 模型可选
EMBEDDING_BASE_URL=https://...
EMBEDDING_API_PATH=/embeddings
EMBEDDING_API_KEY=...
EMBEDDING_MODEL=your-embedding-model
EMBEDDING_DIMENSIONS=1024
EMBEDDING_BATCH_SIZE=10               # provider 限制批量输入时可调低
EMBEDDING_REQUEST_INTERVAL=1s         # provider 限流节奏；429/5xx 会重试
DRAIN3_TOKEN=...
```

密钥只从环境变量或未纳入版本控制的 `.env` 读取。健康检查响应、日志、Trace 和 Benchmark manifest 均不会记录密钥；Docker 构建上下文也会排除 `.env`、运行时凭据和 Benchmark 产物。

所有对话请求均设置 `stream: true`。OpenAI-compatible SSE 会聚合文本 delta 和按索引拆分的 Tool Calling 参数；Anthropic-compatible SSE 会聚合文本块和 `input_json_delta` 工具参数。忽略流式参数并返回普通 JSON 的兼容 provider 仍可使用。进入恢复模式前，系统会通过有界重试探测模型的 Tool Calling 能力。

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
6. 启动 Go Agent。

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

指标包括严格根因准确率、category/service/resource 准确率、Evidence Recall、恢复决策准确率、工具成功率、诊断/恢复延迟、告警分组 Precision/Recall/F1、检索 Recall@K/MRR 和 P50/P95/P99。`PLAN.md` 中的数值仅为示例，报告只使用实际测量结果。

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
- Drain3 不可用时，检查 `docker compose ... logs drain3`；Agent 会将日志证据标记为不可用，不会伪造结果。
- Diagnosis 进入 `NEEDS_ATTENTION` 时，应检查证据错误和模型探测结果，不要降低置信度阈值。
- Benchmark 清理失败时，应先恢复 benchmark namespace 再继续。Runner 会拒绝在脏环境中执行后续 case。
