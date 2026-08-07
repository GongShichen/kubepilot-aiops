---
name: understand-incident
description: Understand an incident without turning alert wording into evidence.
---

# understand-incident

## Preconditions

The Brain enters INTAKE with a server-owned Incident.

## Inputs

Use incident identity, scope, alerts, and known resource references.

## Procedure

1. Summarize the observed impact without asserting a cause.
2. Separate known scope from unresolved resources.
3. Identify broad investigation domains.
4. Record uncertainties that require tools.

## Allowed actions

Use control or resource-exploration capabilities only.

## Output contract

Submit a structured incident understanding with impact, domains, and uncertainties.

## Stop and failure conditions

Stop when scope is clear enough to plan; escalate if the root resource cannot be resolved.

## Handoff

Hand off to plan-investigation or explore-resources.
