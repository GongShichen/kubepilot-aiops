---
name: explore-resources
description: Resolve incident resources and one-hop dependencies through server inventory and topology.
---

# explore-resources

## Preconditions

A plan or uncertainty names a target that is not yet resolved.

## Inputs

Use only server-issued resource references and the incident namespace.

## Procedure

1. Resolve the root service or workload.
2. Discover one-hop dependencies.
3. Distinguish Kubernetes resources from registered external dependencies.
4. Record unresolved targets instead of inventing identities.

## Allowed actions

Use resource discovery, Kubernetes inspection, or topology retrieval tools only.

## Output contract

Return resolved ResourceRef IDs, relationships, and unresolved gaps.

## Output example

```json
{"tool":"query_kubernetes_evidence","arguments":{"intent":"resolve the scoped workload and one-hop dependency identities","expected_observation":["typed ResourceRefs and topology relationships"],"targets":[{"namespace":"<incident-namespace>","service":"<server-service>","kind":"Service"}],"signal_kinds":["workload","service","endpoint_slice"],"window_minutes":5}}
```

## Stop and failure conditions

Stop when requested targets are resolved, out of scope, or unavailable.

## Handoff

Hand off to select-tools, form-hypotheses, or escalate-incident.
