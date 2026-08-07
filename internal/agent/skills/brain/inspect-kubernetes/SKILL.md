---
name: inspect-kubernetes
description: Inspect Kubernetes state through typed, namespace-scoped capabilities.
---

# inspect-kubernetes

## Preconditions

A Kubernetes resource or dependency scope has been resolved.

## Inputs

Use server ResourceRefs, workload state, events, EndpointSlice, configuration, and policy references.

## Procedure

1. Inspect the smallest resource set that answers the current question.
2. Separate state facts, events, configuration, and state changes.
3. Preserve UID and ResourceVersion for recovery planning.
4. Never treat access denial as cluster evidence.

## Allowed actions

Use Kubernetes read-only tools. Read references/kubernetes-resources.md for scope and fact semantics.

## Output contract

Return a typed inspection request with hypotheses and expected observations.

## Stop and failure conditions

Stop when required resource facts exist, scope is denied, or evidence saturates.

## Handoff

Hand off to observation processing, form-hypotheses, or plan-recovery.
