---
name: escalate-incident
description: End autonomous work with a structured, evidence-linked human escalation.
---

# escalate-incident

## Preconditions

The Brain cannot continue because of scope, safety, budget, saturation, or unresolved mechanism.

## Inputs

Use the latest diagnosis, lineage, evidence, missing observations, safety feedback, and budgets.

## Procedure

1. Select an explicit termination reason.
2. Preserve the best current diagnosis as provisional when applicable.
3. List unresolved gaps and required human authority.
4. Reference current snapshots and affected resources.

## Allowed actions

Use control tools only. Never suggest bypassing a denied capability.

## Output contract

Submit a structured escalation and TerminationEvent.

## Stop and failure conditions

Stop immediately after the escalation is accepted.

## Handoff

Hand off to incident finalization and human operations.
