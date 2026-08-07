---
name: synthesize-diagnosis
description: Select and submit the LLM Brain's final diagnosis from the hypothesis ledger.
---

# synthesize-diagnosis

## Preconditions

At least one admitted hypothesis exists and current grounding records are available.

## Server-Owned Inputs

Use hypothesis lineage, model belief, grounding level, coverage, contradictions, and snapshot hashes.

## Procedure

1. Select one current revision.
2. Preserve the selected revision's operational category and explain its mechanism using cited IDs.
3. Preserve uncertainty and model confidence.
4. Mark unsupported conclusions as provisional.
5. Never rewrite Runtime grounding.
6. After persistence, call `validate_diagnosis` with the returned Diagnosis ID. Runtime validation may append grounding and provisional status but cannot change the selected semantics or confidence.

## Allowed Tools

Use diagnosis submission, diagnosis validation, and terminal control capabilities only.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Submit AgentDiagnosis with category, statement, mechanism, revision, target, evidence, validation, and snapshot references, then obtain one DiagnosisValidation before termination or recovery.

## Output Example

Copy immutable semantics and confidence from the selected revision:

```json
{"tool":"submit_diagnosis","arguments":{"intent":"persist the selected evidence-grounded diagnosis","expected_observation":["diagnosis persistence and provisional status"],"hypothesis_id":"<hypothesis-revision-id>","statement":"<exact-selected-revision-statement>","category":"<exact-selected-revision-category>","mechanism":"<exact-selected-revision-mechanism>","targets":[{"namespace":"<incident-namespace>","service":"<server-service>","kind":"Service"}],"model_confidence":0.7,"evidence_ids":["<evidence-id>"],"validation_result_ids":["<current-grounding-id>"]}}
```

On the following turn, validate the persisted object rather than resubmitting it:

```json
{"tool":"validate_diagnosis","arguments":{"intent":"append Runtime grounding and snapshot validation to the immutable diagnosis","expected_observation":["validated or provisional diagnosis status with reason codes"],"diagnosis_id":"<diagnosis-id>"}}
```

## Stop & Failure Conditions

Stop with confident diagnosis, provisional diagnosis, continued investigation, or escalation.

When `budget.tool_calls_exhausted` is true, do not request new Evidence, Retrieval, hypothesis Validation, Reflection, Skills, or Recovery. Use the existing revisions and Grounding to call `submit_diagnosis`, then `validate_diagnosis`; or call `finish_investigation` with `HUMAN_ESCALATION` when the existing state cannot support a diagnosis.

## Handoff

Hand off to plan-recovery only when Runtime permission can be evaluated.
