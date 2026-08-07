---
name: falsify-hypotheses
description: Seek observations that distinguish or refute active hypotheses.
---

# falsify-hypotheses

## Preconditions

At least two admitted hypotheses or one hypothesis with an unresolved falsification condition exists.

## Inputs

Use active revisions, grounding records, topology, missing observations, and tool policy.

## Procedure

1. Identify the smallest discriminating observation.
2. State predictions for the competing hypotheses.
3. Prefer negative or counterfactual evidence when available.
4. Bind the request to all affected hypotheses.
5. Stop if the expected observation cannot change a decision.

## Allowed actions

Use validation, comparison, and read-only evidence capabilities.

## Output contract

Submit a validation/comparison call or a discriminating evidence request.

## Output example

```json
{"tool":"validate_hypothesis","arguments":{"intent":"test the bound mechanism against current cited facts","expected_observation":["Grounding Level, coverage, support, contradiction, and missing observations"],"hypothesis_id":"<hypothesis-revision-id>","supporting_evidence_ids":["<evidence-id>"],"contradicting_evidence_ids":[],"missing_observations":["<unobserved-falsifier>"],"expected_causal_node_ids":["<server-causal-node-id>"]}}
```

## Stop and failure conditions

Stop on decisive validation, evidence saturation, or unavailable scope.

## Handoff

Hand off to observation processing, reflection, or diagnosis synthesis.
