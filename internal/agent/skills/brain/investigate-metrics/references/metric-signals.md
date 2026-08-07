# Metric signal selection

- Request typed signal kinds, never raw query expressions.
- Use a current window and a compatible baseline window.
- Treat empty vectors and incomplete scrape windows as missing observations.
- Prefer normalized utilization, saturation, error, latency, traffic, restart, and dependency signals.
- Bind every post-admission request to the hypotheses it can support or contradict.
- Stop when another metric cannot change GroundingLevel, Coverage, or hypothesis separation.
