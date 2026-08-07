# KubePilot Autonomous SRE Benchmark

`benchmark/` contains only datasets, typed fault injectors, execution adapters, evaluator-only scoring, reproducibility manifests, statistics, and report writers. It calls public production boundaries. Agent orchestration, retrieval, reasoning, memory, causal learning, telemetry, and safety are implemented once outside this directory.

## Diagnosis comparison

The formal comparison uses five production strategies:

| Strategy | Required execution footprint |
|---|---|
| `rule-only` | `eino-rule-diagnosis-runtime`; deterministic signal/assertion candidate baseline with no cognitive model call. |
| `evidence-only` | `eino-evidence-diagnosis-runtime`; deterministic Evidence → Signal → State Assertion → Candidate → Causal/Falsification → Arbitration flow. |
| `cognitive` | `eino-cognitive-diagnosis-runtime`; Evidence-only plus bounded Interpreter and Comparator proposals. |
| `active-diagnosis` | `eino-cognitive-diagnosis-runtime`; Cognitive Runtime plus the two-round, server-valued Investigator loop. |
| `react` | `single-react`; one Diagnosis ReAct agent with live evidence tools and no hierarchy or long-term memory. |

The comparison fails if these footprints collapse into the same trace. All methods share one model profile, temperature, request cap, diagnosis budget, fault/load seeds, recovery controller, approval policy, mutation executor, and verifier.

Formal runs use four explicitly allowlisted worker namespaces. Cases are assigned by a stable hash of case ID, seed, and repetition. Each worker stays serial inside its namespace while workers run concurrently, and a global gate bounds complete diagnosis/recovery workflows. Metrics, logs, traces, and seeded episodic memory remain namespace scoped. The manifest records the worker pool, gate limit, and shard policy.

`BENCHMARK_WORKERS` and `BENCHMARK_MODEL_CONCURRENCY` can lower concurrency. Worker names come from the fixed `BENCHMARK_WORKER_NAMESPACES` pool; arbitrary names are rejected, and the provisioner refuses to adopt an existing namespace without the benchmark-worker label.

## Dataset

`incidents.yaml` expands to 120 typed scenarios across CPU, Memory, Database, Network, Deployment, and Dependency faults, with 20 scenarios per family. Each family is split into four Dev, four Validation, and twelve Test scenarios. The standard formal run evaluates 72 Test scenarios with three paired seeds, producing 216 cases per strategy.

Ground truth remains runner-side. Incident API requests contain observations only and never contain a case ID, scenario ID, injector name, expected evidence, expected action, root cause, or allowed answer. Audit tests enforce the production/benchmark dependency boundary.

## Statistics and validity

- Diagnosis and recovery: paired McNemar test.
- Cost and latency: paired Wilcoxon signed-rank test.
- Core metrics: category-stratified 95% bootstrap confidence intervals.
- Multiple baselines: Holm correction.
- Reports: absolute difference, relative change, effect size, per-family breakdown, and failure list.

Infrastructure failures are excluded from model metrics and listed separately. A rate above 2% invalidates the run. A superiority claim requires the paired KubePilot-minus-best-baseline confidence interval to exclude zero.

## Artifacts

One comparison has one parent Run ID. Strategy, case, seed, and repetition form the case identity. The root contains:

- `manifest.json`: parent reproducibility and randomized strategy order;
- `diagnosis-comparison.json`: systems, confidence intervals, and pairwise tests;
- `diagnosis-systems.csv` and `diagnosis-comparison.csv`;
- `report.md`;
- `failures.json`;
- one subdirectory per strategy with manifest, checkpoint, cases, summary, and detailed CSV artifacts.

Reports consume explicit paths or the parent run's persisted result path. They never scan modification times for a newest result.

## Other suites

- `log_retrieval`: isolated Loki/Drain3/template Recall@K, MRR, NDCG, and latency.
- `incident_retrieval`: semantic, lexical, topology, causal, and optional neural ranking ablations against isolated PostgreSQL/Milvus records.
- `correlation`: public webhook alert grouping.
- `recovery`: proposal, dry-run, approval, execution, verification, and safety invariants.
- `agent_behavior`: persisted production Agent observations.
- `causaldiscovery`: causal pattern-mining precision, recall, F1, false discovery rate, path edit distance, and calibration over positive, negative, counterexample, and out-of-distribution cases.
- `causal_ablation`: paired live `no-causal`, `static-causal`, `learned-causal`, and `full` runs measuring strict RCA, recovery success, and evidence efficiency with confidence intervals and corrected significance tests.
- `evolution`: verified-incident topology and causal knowledge evolution.

## Commands

```bash
make benchmark-validate
make benchmark-standard
make benchmark-causal-ablation-report
make benchmark-full
```

`benchmark/manifests/default.yaml` is the sole checked-in reproducibility contract. Generated reports remain under `artifacts/` and are not source files.
