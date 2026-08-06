# KubePilot: A Causal-Aware Autonomous SRE Agent with Constrained ReAct Architecture

[English](README.md) | [简体中文](README.zh-CN.md)

KubePilot is an Eino-based autonomous SRE control plane for evidence-grounded Kubernetes incident diagnosis and safety-governed recovery. Its core loop is:

> Observation → Hypothesis → Evidence → Causal Graph → Validated Knowledge → Safe Recovery

The project deliberately separates model autonomy from mutation authority. Agents may plan, inspect, challenge hypotheses, and propose recovery; deterministic server code owns evidence identity, budgets, confidence gates, namespace boundaries, dry-run validation, approval, idempotency, execution, and post-action verification.

No checked-in benchmark result claims that KubePilot is better than a baseline. A superiority claim is valid only after a formal paired run produces a positive 95% confidence interval against the best baseline.

## Architecture

```mermaid
flowchart TD
    I["Incident Intake"] --> S["Supervisor / Incident Commander"]
    S --> P["Planner Agent"]
    P --> M["Metric Worker"]
    P --> L["Log Worker"]
    P --> T["Trace Worker"]
    P --> K["Topology / Kubernetes Worker"]
    M --> D["Primary Diagnosis Agent"]
    L --> D
    T --> D
    K --> D
    M --> A["Blind Alternative Agent"]
    L --> A
    T --> A
    K --> A
    D --> C["Critic Agent"]
    A --> C
    C --> R{"Deterministic Arbiter"}
    R -->|"accepted"| REC["Recovery Agent"]
    R -->|"evidence gap; at most one retry"| P
    R -->|"unresolved"| H["NEEDS_ATTENTION"]
    REC --> DR["Kubernetes DryRunAll"]
    DR --> AP["Human Approval Interrupt"]
    AP --> EX["Idempotent Executor"]
    EX --> V["Post-action Verification"]
```

The Supervisor changes phases and enforces global state; it does not invent a root cause. The Planner emits a bounded `InvestigationPlan`. Four read-only workers run concurrently and return findings containing server-issued Evidence IDs. Primary Diagnosis and the first-round Alternative independently produce up to three falsifiable hypotheses. The Critic identifies contradictions and evidence gaps. A deterministic arbiter accepts only an evidence-grounded candidate with score at least `0.80` and a margin of at least `0.15`; debate stops after two rounds.

Only structured `HypothesisArgument`, `Critique`, Evidence IDs, score changes, and `ArbitrationResult` are stored. Chain-of-Thought is never requested or persisted.

## Four real diagnosis strategies

`diagnosis_method` selects a production execution path, not a report label:

| Strategy | Production behavior |
|---|---|
| `direct` | One structured model call over a fixed, server-owned initial evidence packet; no retrieval, memory, or tool loop. |
| `rag` | Direct plus the top five Episodic Memory records; one model call and no live tool loop. |
| `react` | One bounded Diagnosis ReAct agent with Metric, Log, Trace, and Kubernetes tools; no Planner, debate, long-term memory, or causal enhancement. |
| `kubepilot` | Planner, concurrent workers, Primary, blind Alternative, Critic, deterministic arbitration, scoped memory, and causal-aware reasoning. |

Legacy request values are accepted for one compatibility window: `llm-only → direct` and `vector-rag → rag`. Incidents and artifacts persist only canonical IDs. All strategies use the same model settings, deterministic evidence collectors, per-Agent token budget, recovery path, approval policy, executor, and verification controller.

The comparison writer rejects a run when strategy footprints do not differ as specified. This prevents four labels from accidentally measuring the same Agent.

## Memory architecture

```mermaid
flowchart TD
    AM["Agent Memory"] --> W["Working Memory<br/>Redis checkpoint"]
    AM --> E["Episodic Memory<br/>verified incidents + Milvus/PostgreSQL"]
    AM --> S["Semantic Memory<br/>topology + causal patterns"]
    AM --> P["Procedural Memory<br/>versioned SRE Skills"]
```

`MemoryService` provides one read, verified-write, and access-audit boundary. Working memory contains the current plan, evidence, hypotheses, debate, and interruption state and expires seven days after terminal state. Long-term retrieval applies a 90-day half-life, records the query hash, returned IDs, scores, scope, and consuming Agent, and enforces cluster plus namespace isolation. Generic curated procedures and patterns may be explicitly global; learned tenant data is scoped.

Agents have read/proposal access only. The server-side learner accepts a long-term write only for a resolved, approved, successfully verified production incident with at least two independent evidence sources and no unresolved infrastructure error. Evaluation incidents are excluded from learning.

## Causal incident intelligence

KubePilot currently implements **causal-aware reasoning and causal pattern mining**, not unrestricted causal discovery. The canonical model uses:

- Nodes: `cause`, `mechanism`, `symptom`, `observation`, `action`, `outcome`.
- Edges: `causes`, `manifests_as`, `supports`, `contradicts`, `mitigates`, `verifies`, `correlates`.

Actions mitigate causes; they are not effects of symptoms. Verification links actions to outcomes. Topology is propagation context and never becomes a causal edge by itself. Loki, Jaeger, or Kubernetes provenance alone is not an anomaly: deterministic parsing must first establish an abnormal observation.

The former evolving pattern store is migrated into the canonical `causal_patterns` table. A learned candidate requires three independent resolved incidents, average evidence confidence of at least `0.80`, contradiction at most `0.10`, at least two source types, and successful recovery or explicit human confirmation before activation. Candidates move through `candidate → validating → active/rejected`; operators can disable and roll back knowledge.

## Safety-governed recovery

Recovery can propose only `restart_pod`, `scale_deployment`, or `rollback_deployment`. It cannot discover mutation, approval-data, or verification capabilities. Execution requires:

- a proposal validated with Kubernetes `DryRunAll`;
- an unexpired server-generated approval context;
- namespace allowlist membership;
- matching incident, proposal, UID, resource version, and mutation hash;
- an idempotency key;
- deterministic post-action verification with consecutive healthy samples.

Approval bypass, namespace violation, and duplicate mutation are protected invariants. Budget exhaustion, unresolved debate, stale targets, missing approval, or failed verification moves the Incident to explicit attention/failure states instead of forcing a recovery.

## Autonomous SRE Benchmark

The benchmark directory contains datasets, injectors, public-boundary runners, evaluator-only labels, statistics, and report writers. Production orchestration, retrieval, reasoning, memory, causal learning, safety, and telemetry remain outside `benchmark/`; there is no benchmark-only diagnosis implementation.

The catalog expands deterministically to 120 scenarios:

| Fault family | Cases |
|---|---:|
| CPU | 20 |
| Memory | 20 |
| Database | 20 |
| Network | 20 |
| Deployment | 20 |
| Dependency | 20 |

It includes payment latency caused by a payment-pod memory leak, Redis unavailability, real held-session MySQL connection saturation, wrong deployment configuration, CPU throttling, OOM, network policy, probe, image, selector, and dependency variants.

Each family contributes four Dev, four Validation, and twelve hidden Test scenarios. A formal Test comparison runs 72 scenarios with three paired load/fault seeds: 216 paired cases per strategy. Strategy order is deterministically rotated per parent Run ID, and the Kubernetes baseline is restored and checked before every case.

The main table reports Strict Diagnosis Accuracy, Recovery Success, Safety Violations, mean model cost, and P95 latency. Reports also preserve category, variant, service, resource, evidence, calibration, tool-complexity, and failure breakdowns. `ToolCost` is a complexity unit, never currency; monetary cost uses separate prompt, visible completion, reasoning-token, and pricing-snapshot fields for every diagnosis and recovery-proposal model call.

Statistics are fixed in code:

- paired McNemar tests for diagnosis and recovery;
- paired Wilcoxon signed-rank tests for cost and latency;
- category-stratified 95% bootstrap confidence intervals;
- Holm correction across baseline comparisons;
- absolute differences, relative changes, and effect sizes.

Infrastructure failures are separated from model failures. More than 2% infrastructure failure or any protected safety violation invalidates the formal run. The comparison produces a parent manifest, per-strategy manifests, JSON, system/comparison/breakdown CSV tables, Markdown, paired statistics, a failure list, and per-case checkpoints. Reports read exact parent-referenced artifacts and never guess the newest file.

Validate the catalog and manifest without a cluster:

```bash
make benchmark-validate
go run ./cmd/benchmark environment --manifest benchmark/manifests/default.yaml
```

Run the paired formal comparison:

```bash
make benchmark-standard
```

Run the causal-memory ablation through the same production KubePilot path:

```bash
make benchmark-causal-ablation-report
```

This runs `no-causal`, `static-causal`, `learned-causal`, and `full` on paired cases, then reports diagnosis accuracy, recovery success, evidence efficiency, 95% confidence intervals, McNemar/Wilcoxon tests, and Holm-adjusted p-values. It does not substitute an offline proxy for live recovery outcomes.

The Make targets share `BENCHMARK_RUN_ID` and `BENCHMARK_ARTIFACT_DIR`, so downstream Agent, Recovery, Autonomous, and suite reports consume artifacts from that exact run. A full opt-in suite is:

```bash
make benchmark-full
```

Generated results remain under `artifacts/` and are not source-controlled.

## Offline policy improvement

The learning service performs bounded offline policy improvement rather than online reinforcement learning. It may propose changes only to Planner task weights, memory ranking, hypothesis/arbiter weights, and debate gates. A candidate is evaluated on fixed replay data, then may enter shadow mode and staged rollout at 5%, 25%, and 100%.

Promotion requires at least a three-percentage-point strict RCA gain, a positive lower confidence bound, no recovery regression, zero protected safety violations, and cost/latency guardrails. Recovery tool allowlists, namespaces, approvals, mutation implementations, and safety gates are immutable to the learner. Every candidate and evaluation is persisted; rollback points to the previous active policy.

## Runtime stack

- Go, Gin, Eino ADK, and `client-go`.
- Prometheus metrics, Loki logs, Jaeger traces, and Kubernetes topology.
- PostgreSQL audit/business state, Redis checkpoints, and Milvus vector retrieval.
- An independent Log Indexer using Drain3 plus embeddings; Drain3 is not on the incident query path.
- OpenAI-compatible or Anthropic-compatible streaming chat with tool calling; OpenAI-compatible embeddings and optional reranking.

## Configuration and local deployment

```bash
cp .env.example .env
make bootstrap
make doctor
make up
```

At minimum configure `API_TOKEN`, `ALERTMANAGER_WEBHOOK_TOKEN`, the chat endpoint/model/key, and the embedding endpoint/model/key. Optional reranking and per-million-token prices are documented in `.env.example`. Secrets remain in the untracked `.env`; logs, health endpoints, checkpoints, and manifests store hashes or redacted identities.

Useful local endpoints:

| Component | URL |
|---|---|
| KubePilot | http://localhost:8080 |
| Grafana | http://localhost:3000 |
| Prometheus | http://localhost:9090 |
| Alertmanager | http://localhost:9093 |
| Loki | http://localhost:3100 |
| Jaeger | http://localhost:16686 |

`/readyz` checks PostgreSQL, Redis, and the Agent Registry. The benchmark preflight additionally verifies the model, embeddings, reranker when enabled, Prometheus, Loki, Jaeger, Kubernetes access, and a clean baseline.

## API and auditability

The OpenAPI specification is in `api/openapi.yaml`. Relevant endpoints include:

- `POST /api/v1/incidents` with `direct`, `rag`, `react`, or `kubepilot`, plus an optional controlled causal mode.
- `GET /api/v1/incidents/{id}/investigation` for Plan, findings, debate, and arbitration without hidden reasoning.
- `GET /api/v1/incidents/{id}/agent-runs` for strategy, architecture, hierarchy, model usage, tools, budgets, and safety events.
- `POST /api/v1/benchmarks/runs` for strategies, split, seeds, repetitions, model profile, and auto-approval policy.
- `GET /api/v1/benchmarks/runs/{id}/results` for the exact persisted result artifact.
- `POST /api/v1/knowledge/causal-patterns/{id}/rollback` for operator-authenticated restoration of an audited pattern revision.

Model calls record prompt, completion, reasoning tokens, latency, estimated price, phase, Agent, and parent Agent. Memory accesses, hypotheses, confidence transitions, safety feedback, proposals, approvals, mutations, and verification remain auditable without persisting Chain-of-Thought.

## Verification

```bash
go test ./...
go vet ./...
go test -race ./...
```

Architecture tests reject production imports of benchmark packages and benchmark-side copies of production runtime logic. Other tests cover strategy footprint differences, Planner bounds, worker degradation, blind alternatives, debate limits, arbitration, scoped memory, causal edge semantics, learning eligibility, approval interruption, checkpoint resume, idempotency, paired statistics, artifacts, migration-facing store contracts, and compatibility aliases.
