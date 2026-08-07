---
name: verify-recovery
description: Interpret server verification after an approved state change.
---

# verify-recovery

## Preconditions

A confirmed mutation and server verification record exist.

## Inputs

Use expected outcome, verification checks, state-change provenance, and current evidence.

## Procedure

1. Compare every expected outcome with server checks.
2. Distinguish confirmed failure from unknown execution outcome.
3. Do not propose another mutation automatically.
4. Request reflection only for confirmed recoverable failure.
5. Escalate unknown outcomes.

## Allowed actions

Use verification and control capabilities only.

## Output contract

Submit completion, recoverable failure reflection, or escalation.

## Output example

The Runtime owns success/failure termination. For an unknown outcome, request human escalation:

```json
{"tool":"finish_investigation","arguments":{"intent":"escalate an unconfirmed execution outcome without another mutation","expected_observation":["audited human escalation"],"reason":"HUMAN_ESCALATION","hypothesis_id":"<selected-hypothesis-revision-id>","unresolved_gaps":["execution outcome is not confirmed by server verification"]}}
```

## Stop and failure conditions

Stop on verified success, confirmed failure handoff, or unknown outcome escalation.

## Handoff

Hand off to finalization, reflection, or escalation.
