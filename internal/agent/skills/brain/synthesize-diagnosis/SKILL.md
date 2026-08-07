---
name: synthesize-diagnosis
description: Select and submit the LLM Brain's final diagnosis from the hypothesis ledger.
---

# synthesize-diagnosis

## Preconditions

At least one admitted hypothesis exists and current grounding records are available.

## Inputs

Use hypothesis lineage, model belief, grounding level, coverage, contradictions, and snapshot hashes.

## Procedure

1. Select one current revision.
2. Preserve the selected revision's operational category and explain its mechanism using cited IDs.
3. Preserve uncertainty and model confidence.
4. Mark unsupported conclusions as provisional.
5. Never rewrite Runtime grounding.

## Allowed actions

Use diagnosis submission and terminal control capabilities only.

## Output contract

Submit AgentDiagnosis with category, statement, mechanism, revision, target, evidence, validation, and snapshot references.

## Stop and failure conditions

Stop with confident diagnosis, provisional diagnosis, continued investigation, or escalation.

## Handoff

Hand off to plan-recovery only when Runtime permission can be evaluated.
