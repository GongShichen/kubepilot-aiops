---
name: plan-investigation
description: Build a bounded evidence-seeking plan for the current incident.
---

# plan-investigation

## Preconditions

Incident understanding exists and the Brain is in PLANNING.

## Inputs

Use current scope, admitted hypotheses when present, prior tool history, and remaining budget.

## Procedure

1. State the decision the investigation must enable.
2. Prefer observations that separate plausible explanations.
3. Bind later requests to admitted hypotheses.
4. Include stop conditions for saturation and safety.
5. Avoid repeating completed or rejected requests.

## Allowed actions

Use planning and control capabilities; do not emit collector-specific query syntax.

## Output contract

Submit an investigation plan with goals, priorities, evidence needs, and stop conditions.

## Output example

```json
{"tool":"submit_investigation_plan","arguments":{"intent":"create a bounded discriminating investigation","expected_observation":["server acceptance of the investigation plan"],"objective":"separate local, dependency, and infrastructure explanations","goals":["form a bounded competing hypothesis set","collect one discriminating current observation"],"stop_conditions":["evidence saturates","scope or safety blocks further collection"]}}
```

## Stop and failure conditions

Stop when at least one executable evidence goal exists or escalation is required.

## Handoff

Hand off to explore-resources, select-tools, or form-hypotheses.
