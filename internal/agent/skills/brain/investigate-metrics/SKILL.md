---
name: investigate-metrics
description: Investigate metric evidence using bounded server-compiled queries.
---

# investigate-metrics

## Preconditions

A metric evidence need and resolved target exist.

## Inputs

Use hypotheses, expected observations, time window, and metric signal reference.

## Procedure

1. Identify the metric state that would support or contradict each bound hypothesis.
2. Request current and baseline observations through typed signal kinds.
3. Prefer independent metrics over repeated variants.
4. Treat empty or stale results as missing observation, not healthy evidence.

## Allowed actions

Use metric evidence tools only. Read references/metric-signals.md when selecting signal kinds.

## Output contract

Return an AgentActionIntent with target, hypothesis IDs, evidence need, and expected observation.

## Stop and failure conditions

Stop after a decisive observation, two no-information results, or policy rejection.

## Handoff

Hand off to observation processing or select-tools.
