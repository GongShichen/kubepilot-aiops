#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
compose=(docker compose -f deploy/docker/docker-compose.yml)

# Restore Kubernetes objects first; these selectors are dedicated to the
# benchmark namespace and cannot match user workloads outside it.
kubectl delete jobs -n kubepilot-benchmark -l kubepilot.io/scenario --ignore-not-found
kubectl delete networkpolicies -n kubepilot-benchmark -l kubepilot.io/scenario --ignore-not-found
kubectl scale deployment/gateway-service deployment/order-service deployment/payment-service deployment/mysql --replicas=1 -n kubepilot-benchmark
kubectl rollout restart deployment/gateway-service deployment/order-service deployment/payment-service -n kubepilot-benchmark

# Stop consumers before replacing only the explicitly named observability
# cache volumes. PostgreSQL, Redis persistence and Milvus history remain.
"${compose[@]}" stop kubepilot-agent prometheus alertmanager loki jaeger drain3 >/dev/null
"${compose[@]}" rm -sf kubepilot-agent prometheus alertmanager loki jaeger drain3 drain3-init >/dev/null
for volume in kubepilot_prometheus-data kubepilot_alertmanager-data kubepilot_loki-data kubepilot_drain3-data; do
  if docker volume inspect "$volume" >/dev/null 2>&1; then
    docker volume rm "$volume" >/dev/null
  fi
done

# Redis is short-term Agent state by design. PostgreSQL deletion is scoped to
# benchmark incidents/runs; non-benchmark incidents and the history corpus are
# intentionally preserved.
"${compose[@]}" exec -T agent-redis redis-cli FLUSHDB >/dev/null
"${compose[@]}" exec -T postgres psql -v ON_ERROR_STOP=1 -U kubepilot -d kubepilot <<'SQL' >/dev/null
DELETE FROM incidents WHERE namespace = 'kubepilot-benchmark';
DELETE FROM benchmark_runs;
SQL

"${compose[@]}" up -d prometheus alertmanager loki jaeger drain3 kubepilot-agent >/dev/null
for attempt in $(seq 1 60); do
  if curl -fsS http://localhost:8080/readyz >/dev/null \
    && curl -fsS http://localhost:3100/ready >/dev/null \
    && curl -fsS http://localhost:9090/-/ready >/dev/null; then
    break
  fi
  if [[ "$attempt" -eq 60 ]]; then
    echo "diagnosis infrastructure did not become ready" >&2
    exit 1
  fi
  sleep 2
done

kubectl rollout status deployment/gateway-service -n kubepilot-benchmark --timeout=120s >/dev/null
kubectl rollout status deployment/order-service -n kubepilot-benchmark --timeout=120s >/dev/null
kubectl rollout status deployment/payment-service -n kubepilot-benchmark --timeout=120s >/dev/null
kubectl rollout status deployment/mysql -n kubepilot-benchmark --timeout=120s >/dev/null

echo "diagnosis benchmark caches cleared and baseline restored"
