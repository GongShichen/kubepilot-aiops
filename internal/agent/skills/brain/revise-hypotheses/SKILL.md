---
name: revise-hypotheses
description: Revise belief through immutable hypothesis lineage.
---

# revise-hypotheses

## Preconditions

A GroundingDelta or Reflection identifies a material change.

## Inputs

Use parent revisions, BeliefDelta, cited evidence, and validation IDs.

## Procedure

1. Choose REFINE, REPLACE, SPLIT, or MERGE.
2. Preserve every parent ID.
3. Explain RevisionReason using cited records.
4. Create a new revision instead of mutating a terminal revision.
5. Respect revision and branch budgets.

## Allowed actions

Use revise-hypothesis and belief-commit capabilities only.

## Output contract

Submit a new immutable revision and any confidence update separately.

## Stop and failure conditions

Stop when the new revision is admitted, rejected, or budgets prohibit another branch.

## Handoff

Hand off to investigation, diagnosis synthesis, or escalation.
