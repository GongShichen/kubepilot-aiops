---
name: investigate-traces
description: Investigate trace evidence and error propagation using server-issued span identities.
---

# investigate-traces

## Preconditions

A trace evidence need and resolved service or dependency exists.

## Inputs

Use current hypotheses, topology, time window, and trace signal reference.

## Procedure

1. Seek error or latency spans on the relevant path.
2. Preserve upstream and downstream target identity.
3. Use normal spans as counterevidence only when scope and time align.
4. Do not infer causality from adjacency alone.

## Allowed actions

Use trace evidence tools only. Read references/trace-signals.md when choosing observations.

## Output contract

Return a trace request with target, bound hypotheses, and expected path observation.

## Stop and failure conditions

Stop when the path is distinguished, unavailable, or saturated.

## Handoff

Hand off to observation processing or falsify-hypotheses.
