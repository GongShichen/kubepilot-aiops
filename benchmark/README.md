# KubePilot Autonomous SRE Benchmark

`benchmark/` owns only datasets, suite execution, evaluator-side scoring,
manifests, and report generation. Agent orchestration, budgets, telemetry,
retrieval ranking, reasoning, safety, and knowledge evolution are implemented
once in production packages. Benchmarks invoke those public production
boundaries and never maintain benchmark-only variants.

The benchmark is split by capability. The 500,000-record dataset belongs only
to `log_retrieval` and measures Loki/Drain3/template retrieval. Historical
incident ranking uses structured incident records under
`datasets/incidents/`; its topology and causal signals are reranking features,
not log-template labels. Its runner calls the production
`retrieval.IncidentRetrievalEngine` against isolated PostgreSQL and Milvus
records. Log-template evaluation calls the production
`retrieval.RankLogTemplates` capability. Diagnosis and Recovery use the public
Incident API, while Agent Behavior and Knowledge Evolution consume
evaluator-only observations.

## Suites

- `log_retrieval`: Loki/Drain3/template Recall@K, MRR, NDCG and latency.
- `incident_retrieval`: semantic, semantic+lexical, topology rerank, causal
  rerank and full neural ablation metrics.
- `diagnosis`: public Agent lifecycle, RCA, evidence attribution, hypotheses,
  causal path coverage and tool efficiency.
- `recovery`: proposal, dry-run, approval, execution and verification safety.
- `agent_behavior`: constrained ReAct iteration/tool/correction/budget
  observations emitted by the production runtime.
- `evolution`: resolved-incident topology and causal knowledge evolution.

`manifests/autonomous.yaml` is the reproducibility contract. Expected root causes,
related incidents and evolution labels are evaluator-only and are never
returned by `AgentContext`.

Legacy compatibility runners and duplicate ranking implementations have been
removed. Log retrieval never evaluates topology or causal reasoning.
