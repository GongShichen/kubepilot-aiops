---
name: handle-safety-feedback
description: Respond to safety and constraint feedback without bypassing it.
---

# handle-safety-feedback

## Preconditions

The Runtime returns repairable, fatal, or human-required feedback.

## Server-Owned Inputs

Use failed checks, missing requirements, current authority, and remaining correction budget.

## Procedure

1. Classify the feedback.
2. Revise only within existing authority.
3. Refresh stale evidence or target versions when allowed.
4. Never infer the missing answer from feedback.
5. Escalate fatal or human-required conditions.

## Allowed Tools

Use planning, read-only, recovery proposal, or control tools allowed by the current phase.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Return a corrected intent/plan or an escalation action.

## Output Example

For a human-required or non-repairable boundary:

```json
{"tool":"finish_investigation","arguments":{"intent":"preserve the current grounded state and request human authority","expected_observation":["audited human escalation"],"reason":"HUMAN_ESCALATION","hypothesis_id":"<current-hypothesis-revision-id>","unresolved_gaps":["<server-reported-safety-gap>"]}}
```

## Stop & Failure Conditions

Stop when feedback is satisfied, corrections are exhausted, or human authority is required.

## Handoff

Hand off to the relevant phase Skill or escalate-incident.
