---
name: understand-incident
description: Understand an incident without turning alert wording into evidence.
---

# understand-incident

## Preconditions

The Brain enters INTAKE with a server-owned Incident.

## Server-Owned Inputs

Use incident identity, scope, alerts, and known resource references.

## Procedure

1. Summarize the observed impact without asserting a cause.
2. Separate known scope from unresolved resources.
3. Identify broad investigation domains.
4. Record uncertainties that require tools.

## Allowed Tools

Use control or resource-exploration capabilities only.

## Required IDs

Every Incident, Resource, Evidence, Hypothesis Revision, Validation, Diagnosis, Proposal, Approval, Snapshot, and Tool Call identifier used by this Skill must be copied from the current server-owned context or a Tool result. Never synthesize an identifier.

## Output Contract

Submit a structured incident understanding with impact, domains, and uncertainties.

## Output Example

```json
{"tool":"submit_incident_understanding","arguments":{"intent":"record impact without asserting a cause","expected_observation":["server acceptance of the incident understanding"],"summary":"Observed service degradation requires investigation","affected_targets":[{"namespace":"<incident-namespace>","service":"<server-service>","kind":"Service"}],"possible_domains":["application","dependency","infrastructure"],"unknowns":["failure mechanism is not yet observed"]}}
```

## Stop & Failure Conditions

Stop when scope is clear enough to plan; escalate if the root resource cannot be resolved.

## Handoff

Hand off to plan-investigation or explore-resources.
