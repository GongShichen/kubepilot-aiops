---
name: select-tools
description: Select a bounded tool category and operation for an explicit information need.
---

# select-tools

## Preconditions

The Brain has an investigation goal and an active phase Skill.

## Inputs

Use the allowed tool catalogue, tool policy, prior fingerprints, and budget.

## Procedure

1. State the intent.
2. Bind admitted hypotheses when required.
3. Name the expected observation.
4. Select the smallest compatible tool category.
5. Avoid exact repeats and no-information streaks.

## Allowed actions

Request only tools exposed by the Runtime. Do not compose raw PromQL, LogQL, kubectl, shell, or arbitrary filters.

## Output contract

Emit one or more read-only calls in one category, or one serial reasoning/control call.

## Output example

Activate the required optional Skill before selecting its category:

```json
{"tool":"request_skills","arguments":{"intent":"load the metric investigation procedure","expected_observation":["Skill activation decision"],"skill_ids":["investigate-metrics"],"reason":"a metric observation can distinguish the active hypotheses","trigger":"HYPOTHESIS_CONFLICT"}}
```

```json
{"tool":"select_tool_category","arguments":{"intent":"route the next turn to bounded evidence collection","expected_observation":["EVIDENCE category activation"],"category":"EVIDENCE"}}
```

## Stop and failure conditions

Stop when no policy-compliant call can add information.

## Handoff

Hand off to the selected evidence, retrieval, reasoning, or control Skill.
