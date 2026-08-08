---
name: falsify-hypotheses
description: Seek observations that distinguish or refute active hypotheses.
---

# falsify-hypotheses

## Preconditions

At least two admitted hypotheses or one hypothesis with an unresolved falsification condition exists.

## Server-Owned Inputs

Use active revisions, grounding records, topology, missing observations, and tool policy.

## Procedure

1. Identify the smallest discriminating observation.
2. State predictions for the competing hypotheses.
3. Prefer negative or counterfactual evidence when available.
4. Attribute every current Evidence ID collected for the bound Hypothesis explicitly as `SUPPORT`, `CONTRADICT`, or `NEUTRAL`, with a bounded weight and concise relation reason; omission is a contract failure.
5. Bind the request to all affected hypotheses.
6. Stop if the expected observation cannot change a decision.

## Allowed Tools

Use validation, comparison, and read-only evidence capabilities.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Submit a validation/comparison call or a discriminating evidence request.

## Output Example

```json
{"tool":"validate_hypothesis","arguments":{"intent":"test the bound mechanism against current cited facts","expected_observation":["frozen Evidence attribution plus Grounding Level, coverage, support, contradiction, and missing observations"],"hypothesis_id":"<hypothesis-revision-id>","attributions":[{"evidence_id":"<evidence-id>","relation":"SUPPORT","weight":0.9,"reason":"the current observation matches this hypothesis prediction"},{"evidence_id":"<other-evidence-id>","relation":"NEUTRAL","weight":0.2,"reason":"the observation is current but does not distinguish this mechanism"}],"missing_observations":["<unobserved-falsifier>"],"expected_causal_node_ids":["<server-causal-node-id>"]}}
```

## Stop & Failure Conditions

Stop on decisive validation, evidence saturation, or unavailable scope.

## Handoff

Hand off to observation processing, reflection, or diagnosis synthesis.
