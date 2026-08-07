# Kubernetes resource and scope rules

- Inspect only resources resolved in the Incident namespace.
- One-hop external dependencies may be read only through registered inventory capabilities.
- Preserve UID and ResourceVersion whenever a resource may later be a recovery target.
- Classify events, workload state, EndpointSlice state, configuration, and policy effects separately.
- Treat RBAC, admission, timeout, and API errors as Constraint or Error results, never cluster evidence.
- Mutations remain unavailable until recovery permission, dry-run, and approval complete.
