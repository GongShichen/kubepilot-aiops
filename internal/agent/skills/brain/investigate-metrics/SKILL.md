---
name: investigate-metrics
description: Investigate metric evidence using bounded server-compiled queries.
---

# investigate-metrics

## Preconditions

A metric evidence need and resolved target exist.

## Server-Owned Inputs

Use hypotheses, expected observations, time window, and metric signal reference.

## Procedure

1. Identify the metric state that would support or contradict each bound hypothesis.
2. Request current and baseline observations through typed signal kinds.
3. Prefer independent metrics over repeated variants.
4. Treat empty or stale results as missing observation, not healthy evidence.

## Allowed Tools

Use metric evidence tools only. Read references/metric-signals.md when selecting signal kinds.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Return an AgentActionIntent with target, hypothesis IDs, evidence need, and expected observation.

## Output Example

```json
{"tool":"query_metrics","arguments":{"intent":"distinguish the bound resource and dependency hypotheses","expected_observation":["current and baseline server-normalized metric states"],"targets":[{"namespace":"<incident-namespace>","service":"<server-service>","kind":"Service"}],"hypothesis_ids":["<hypothesis-revision-id>"],"evidence_need":["current resource pressure state"],"signal_kinds":["cpu","memory","request_rate"],"window_minutes":5}}
```

## Stop & Failure Conditions

Stop after a decisive observation, two no-information results, or policy rejection.

## Handoff

Hand off to observation processing or select-tools.
