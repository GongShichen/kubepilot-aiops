---
name: investigate-logs
description: Investigate log evidence without treating repeated or unclassified text as proof.
---

# investigate-logs

## Preconditions

A log evidence need and resolved target exist.

## Inputs

Use hypotheses, time window, known templates, and log signal reference.

## Procedure

1. Seek severe, contextualized templates tied to the target and window.
2. Prefer distinct templates over repeated lines.
3. Request counterexamples when a hypothesis predicts their absence.
4. Treat unclassified text as a low-reliability clue.

## Allowed actions

Use log evidence tools only. Read references/log-signals.md for reliability rules.

## Output contract

Return a bounded log evidence request tied to hypotheses and expected observations.

## Stop and failure conditions

Stop after decisive templates, two no-information results, or policy rejection.

## Handoff

Hand off to observation processing or select-tools.
