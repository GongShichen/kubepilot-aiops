# Trace evidence semantics

- Use server-issued trace and span IDs.
- Preserve caller, callee, operation, status, duration, and time window.
- Treat adjacency as topology, not causality.
- Use an error or latency propagation path only when its target identities and timestamps align.
- A normal span contradicts a hypothesis only when it covers the same request class and incident window.
