# KubePilot Autonomous SRE Benchmark

The benchmark is split by capability. The 500,000-record dataset belongs only
to `log_retrieval` and measures Loki/Drain3/template retrieval. Historical
incident ranking uses structured incident records under
`datasets/incidents/`; its topology and causal signals are reranking features,
not log-template labels. Diagnosis and Recovery use the public Incident API,
while Agent Behavior and Knowledge Evolution consume evaluator-only result
envelopes.

## Suites

- `log_retrieval`: Loki/Drain3/template Recall@K, MRR, NDCG and latency.
- `incident_retrieval`: semantic, semantic+lexical, topology rerank, causal
  rerank and full neural ablation metrics.
- `diagnosis`: public Agent lifecycle, RCA, evidence attribution, hypotheses,
  causal path coverage and tool efficiency.
- `recovery`: proposal, dry-run, approval, execution and verification safety.
- `agent`: constrained ReAct tool/cost/correction/budget behavior.
- `evolution`: resolved-incident topology and causal knowledge evolution.

`manifests/autonomous.yaml` is the reproducibility contract. Expected root causes,
related incidents and evolution labels are evaluator-only and are never
returned by `AgentContext`.

Historical compatibility code is not a CLI entry point. New runs use only the
capability-specific packages and commands above; log retrieval never evaluates
topology or causal reasoning.
