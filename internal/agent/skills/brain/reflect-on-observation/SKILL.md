---
name: reflect-on-observation
description: Reflect on material grounding, constraint, tool, recovery, or verification changes.
---

# reflect-on-observation

## Preconditions

The reflection router supplies a valid trigger and remaining reflection budget.

## Inputs

Use only the triggering Tool Result, GroundingDelta, affected hypotheses, and current budget.

## Procedure

1. Explain which belief is affected.
2. Propose a confidence change without changing server grounding.
3. Decide whether a new revision is required.
4. Select the next bounded goal or escalation.
5. Avoid repeating the observation verbatim.

## Allowed actions

Use reflection submission, belief commit, hypothesis revision, or control capabilities only.

## Output contract

Return structured BeliefDelta records and a next goal; do not expose hidden reasoning.

## Stop and failure conditions

Stop when belief changes are committed or the trigger cannot be resolved safely.

## Handoff

Hand off to revise-hypotheses, select-tools, plan-recovery, or escalate-incident.
