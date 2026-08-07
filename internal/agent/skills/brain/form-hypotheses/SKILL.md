---
name: form-hypotheses
description: Create open-world, falsifiable incident hypotheses owned by the LLM Brain.
---

# form-hypotheses

## Preconditions

The Brain has an incident understanding and at least one resolvable target or unresolved mechanism.

## Server-Owned Inputs

Use current observations, resource IDs, uncertainties, and budgets.

## Procedure

1. State an operational category, a concrete cause, and a mechanism without claiming proof.
2. Bind resolvable targets.
3. Declare evidence needs and falsification conditions.
4. Assign model confidence as belief only.
5. Keep hypotheses distinct and within branch limits.

## Allowed Tools

Use hypothesis submission capabilities only. The Runtime may check format, verifiability, scope, and safety, never plausibility.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Submit PROPOSED hypotheses with ROOT relation, category, statement, mechanism, targets, evidence needs, falsification conditions, model confidence, and complete AgentActionIntent metadata.

## Output Example

```json
{"tool":"submit_hypotheses","arguments":{"intent":"create a bounded falsifiable competing set","expected_observation":["admission and resolved scope decisions"],"hypotheses":[{"statement":"The observed degradation is caused by a local execution stall","category":"application","mechanism":"execution stall","targets":[{"namespace":"<incident-namespace>","service":"<server-service>","kind":"Service"}],"evidence_needs":["current workload execution evidence"],"falsification_conditions":["current workload execution is healthy"],"model_confidence":0.5}]}}
```

## Stop & Failure Conditions

Stop when a bounded competing set exists or no hypothesis is verifiable.

## Handoff

Hand off to Admission results, select-tools, or escalate-incident.
