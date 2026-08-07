---
name: escalate-incident
description: End autonomous work with a structured, evidence-linked human escalation.
---

# escalate-incident

## Preconditions

The Brain cannot continue because of scope, safety, budget, saturation, or unresolved mechanism.

## Server-Owned Inputs

Use the latest diagnosis, lineage, evidence, missing observations, safety feedback, and budgets.

## Procedure

1. Select an explicit termination reason.
2. Preserve the best current diagnosis as provisional when applicable.
3. List unresolved gaps and required human authority.
4. Reference current snapshots and affected resources.

## Allowed Tools

Use control tools only. Never suggest bypassing a denied capability.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Submit a structured escalation and TerminationEvent.

## Output Example

```json
{"tool":"finish_investigation","arguments":{"intent":"end autonomous work and preserve the evidence-linked handoff","expected_observation":["persisted TerminationEvent"],"reason":"HUMAN_ESCALATION","hypothesis_id":"<best-current-revision-id>","unresolved_gaps":["<missing-observation-or-authority>"]}}
```

## Stop & Failure Conditions

Stop immediately after the escalation is accepted.

## Handoff

Hand off to incident finalization and human operations.
