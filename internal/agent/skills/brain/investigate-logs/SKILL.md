---
name: investigate-logs
description: Investigate log evidence without treating repeated or unclassified text as proof.
---

# investigate-logs

## Preconditions

A log evidence need and resolved target exist.

## Server-Owned Inputs

Use hypotheses, time window, known templates, and log signal reference.

## Procedure

1. Seek severe, contextualized templates tied to the target and window.
2. Prefer distinct templates over repeated lines.
3. Request counterexamples when a hypothesis predicts their absence.
4. Treat unclassified text as a low-reliability clue.

## Allowed Tools

Use log evidence tools only. Read references/log-signals.md for reliability rules.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Return a bounded log evidence request tied to hypotheses and expected observations.

## Output Example

```json
{"tool":"search_logs","arguments":{"intent":"seek a distinct target-bound failure template","expected_observation":["classified severe templates or an explicit no-information result"],"targets":[{"namespace":"<incident-namespace>","service":"<server-service>","kind":"Service"}],"hypothesis_ids":["<hypothesis-revision-id>"],"evidence_need":["application failure signature"],"signal_kinds":["error","timeout"],"window_minutes":5}}
```

## Stop & Failure Conditions

Stop after decisive templates, two no-information results, or policy rejection.

## Handoff

Hand off to observation processing or select-tools.
