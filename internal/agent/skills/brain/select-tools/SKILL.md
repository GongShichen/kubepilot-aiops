---
name: select-tools
description: Select a bounded tool category and operation for an explicit information need.
---

# select-tools

## Preconditions

The Brain has an investigation goal and an active phase Skill.

## Server-Owned Inputs

Use the allowed tool catalogue, tool policy, prior fingerprints, and budget.

## Procedure

1. State the intent.
2. Bind admitted hypotheses when required.
3. Name the expected observation.
4. Select the smallest compatible tool category.
5. Avoid exact repeats and no-information streaks.

## Allowed Tools

Request only tools exposed by the Runtime. Do not compose raw PromQL, LogQL, kubectl, shell, or arbitrary filters.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Emit one or more read-only calls in one category, or one serial reasoning/control call.

## Output Example

Request the exact optional Skill and its category atomically. The Runtime
validates the Skill dependency graph and verifies that the requested Skill
actually grants the requested category. It never guesses a Skill from the
category:

```json
{"tool":"select_tool_category","arguments":{"intent":"route the next turn to bounded metric collection","expected_observation":["metric Skill activation and EVIDENCE category selection"],"category":"EVIDENCE","skill_ids":["investigate-metrics"],"reason":"a metric observation can distinguish the active hypotheses","trigger":"HYPOTHESIS_CONFLICT"}}
```

Use `request_skills` separately only when repairing a prior constraint from a
Reflection turn and no category transition can yet be selected.

## Stop & Failure Conditions

Stop when no policy-compliant call can add information.

## Handoff

Hand off to the selected evidence, retrieval, reasoning, or control Skill.
