---
name: investigate-traces
description: Investigate trace evidence and error propagation using server-issued span identities.
---

# investigate-traces

## Preconditions

A trace evidence need and resolved service or dependency exists.

## Server-Owned Inputs

Use current hypotheses, topology, time window, and trace signal reference.

## Procedure

1. Seek error or latency spans on the relevant path.
2. Preserve upstream and downstream target identity.
3. Use normal spans as counterevidence only when scope and time align.
4. Do not infer causality from adjacency alone.

## Allowed Tools

Use trace evidence tools only. Read references/trace-signals.md when choosing observations.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Return a trace request with target, bound hypotheses, and expected path observation.

## Output Example

```json
{"tool":"query_traces","arguments":{"intent":"locate latency or error propagation on the resolved path","expected_observation":["server-issued upstream and downstream span facts"],"targets":[{"namespace":"<incident-namespace>","service":"<server-service>","kind":"Service"}],"hypothesis_ids":["<hypothesis-revision-id>"],"evidence_need":["request path failure location"],"signal_kinds":["latency","error"],"window_minutes":5}}
```

## Stop & Failure Conditions

Stop when the path is distinguished, unavailable, or saturated.

## Handoff

Hand off to observation processing or falsify-hypotheses.
