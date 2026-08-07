---
name: plan-recovery
description: Plan one safe recovery and bounded alternatives without executing mutations.
---

# plan-recovery

## Preconditions

A diagnosis exists and the Brain is in RECOVERY.

## Inputs

Use the selected diagnosis, current target UID/version, allowed actions, and safety feedback.

## Procedure

1. Define the recovery goal.
2. Select one reversible primary action.
3. State expected outcome, rollback, verification, and risk.
4. List at most three alternatives for review only.
5. Bind diagnosis and snapshot versions.

## Allowed actions

Use recovery proposal capabilities only. Never execute, approve, or forge execution context.

## Output contract

Submit AgentRecoveryPlan; alternatives must not be scheduled automatically.

## Output example

```json
{"tool":"submit_recovery_plan","arguments":{"intent":"propose one reversible registered recovery for Safety Kernel review","expected_observation":["permission and planning validation status"],"goal":"restore the diagnosed target while limiting blast radius","primary_action":{"action":"restart_pod","target":"<server-resolved-target>","reason":"apply the selected diagnosis recovery plan"},"alternatives":[],"expected_outcome":"the declared verification checks return to their healthy range","rollback_plan":"stop and escalate if the new replica state does not converge","verification_plan":"run the server-owned post-action checks against the same target","risk_reason":"a restart changes workload state and may reduce temporary capacity"}}
```

## Stop and failure conditions

Stop when the plan is accepted for permission, blocked, or requires human action.

## Handoff

Hand off to recovery permission or handle-safety-feedback.
