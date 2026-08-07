---
name: form-hypotheses
description: Create open-world, falsifiable incident hypotheses owned by the LLM Brain.
---

# form-hypotheses

## Preconditions

The Brain has an incident understanding and at least one resolvable target or unresolved mechanism.

## Inputs

Use current observations, resource IDs, uncertainties, and budgets.

## Procedure

1. State an operational category, a concrete cause, and a mechanism without claiming proof.
2. Bind resolvable targets.
3. Declare evidence needs and falsification conditions.
4. Assign model confidence as belief only.
5. Keep hypotheses distinct and within branch limits.

## Allowed actions

Use hypothesis submission capabilities only. The Runtime may check format, verifiability, scope, and safety, never plausibility.

## Output contract

Submit PROPOSED hypotheses with ROOT relation, category, statement, mechanism, targets, evidence needs, falsification conditions, model confidence, and complete AgentActionIntent metadata.

## Stop and failure conditions

Stop when a bounded competing set exists or no hypothesis is verifiable.

## Handoff

Hand off to Admission results, select-tools, or escalate-incident.
