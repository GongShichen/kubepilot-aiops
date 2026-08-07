---
name: explore-resources
description: Resolve incident resources and one-hop dependencies through server inventory and topology.
---

# explore-resources

## Preconditions

A plan or uncertainty names a target that is not yet resolved.

## Server-Owned Inputs

Use only server-issued resource references and the incident namespace.

## Procedure

1. Resolve the root service or workload.
2. Discover one-hop dependencies.
3. Distinguish Kubernetes resources from registered external dependencies.
4. Record unresolved targets instead of inventing identities.

## Allowed Tools

Use resource discovery, Kubernetes inspection, or topology retrieval tools only.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Return resolved ResourceRef IDs, relationships, and unresolved gaps.

## Output Example

```json
{"tool":"discover_resources","arguments":{"intent":"resolve the scoped workload and one-hop dependency identities","expected_observation":["typed ResourceRefs and topology relationships"],"targets":[{"namespace":"<incident-namespace>","service":"<server-service>","kind":"Service"}],"signal_kinds":["workload","service","endpoint_slice"],"window_minutes":5}}
```

## Stop & Failure Conditions

Stop when requested targets are resolved, out of scope, or unavailable.

## Handoff

Hand off to select-tools, form-hypotheses, or escalate-incident.
