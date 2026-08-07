---
name: reflect-on-observation
description: Reflect on material grounding, constraint, tool, recovery, or verification changes.
---

# reflect-on-observation

## Preconditions

The reflection router supplies a valid trigger and remaining reflection budget.

## Server-Owned Inputs

Use only the triggering Tool Result, GroundingDelta, affected hypotheses, and current budget.

## Procedure

1. Classify the trigger as evidence, grounding, tool, constraint, recovery, or verification feedback.
2. Read the complete Assistant Tool-Call summary and its classified Tool Result; never infer an Incident fact from a constraint or execution error.
3. If no admitted hypothesis exists yet, repair the procedure by submitting falsifiable hypotheses or requesting the missing phase-compatible Skill. Do not emit a BeliefDelta for a hypothesis that does not exist.
4. If an admitted hypothesis exists, explain which belief is affected and propose a confidence change without changing server grounding.
5. Decide whether a new revision is required. Changes to Statement, Mechanism, Target, or falsification conditions require a new revision.
6. Select one bounded next goal or escalation. A denied Tool Category requires a compatible Skill activation before category selection.
7. Never retry an unchanged constraint request, and do not repeat the observation verbatim.

## Allowed Tools

Use hypothesis submission when no hypothesis exists; otherwise use reflection submission, belief commit, hypothesis revision, Skill request, or control capabilities only.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Return a structured corrective action. When a hypothesis exists, return BeliefDelta records and a next goal. When none exists, return a hypothesis submission or Skill request that resolves the procedural blocker. Do not expose hidden reasoning.

## Output Example

With an admitted hypothesis, commit only the subjective confidence change:

```json
{"tool":"commit_belief_delta","arguments":{"intent":"update belief after current counterevidence","expected_observation":["server acceptance of the BeliefDelta"],"hypothesis_id":"<hypothesis-revision-id>","new_confidence":0.3,"direction":"DECREASE","evidence_ids":["<evidence-id>"],"validation_result_ids":["<grounding-id>"],"revision_required":false}}
```

Without an admitted hypothesis, use the `submit_hypotheses` example from `form-hypotheses` or request the missing Skill; do not invent a hypothesis ID.

## Stop & Failure Conditions

Stop when belief changes are committed, the missing Skill or hypothesis is submitted, or the trigger cannot be resolved safely. Never claim success after an empty or unclassified Tool Result.

## Handoff

Hand off to revise-hypotheses, select-tools, plan-recovery, or escalate-incident.
