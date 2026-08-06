SHELL := /bin/sh
COMPOSE := docker compose -f deploy/docker/docker-compose.yml
BENCHMARK_RUN_ID ?= $(shell date -u +%Y%m%dT%H%M%SZ)
BENCHMARK_ARTIFACT_DIR ?= artifacts/benchmark/runs/$(BENCHMARK_RUN_ID)
BENCHMARK_WORKERS ?= 4
BENCHMARK_MODEL_CONCURRENCY ?= 4
BENCHMARK_WORKER_NAMESPACES ?= kubepilot-benchmark-worker-01,kubepilot-benchmark-worker-02,kubepilot-benchmark-worker-03,kubepilot-benchmark-worker-04

.PHONY: doctor bootstrap cluster-up infra-up demo-up runtime up down destroy test lint migrate benchmark-validate benchmark-history-seed benchmark-smoke benchmark-reset-diagnosis benchmark-standard benchmark-causal-ablation benchmark-causal-ablation-report benchmark-correlation benchmark-log-retrieval benchmark-incident-retrieval benchmark-component benchmark-agent benchmark-agent-report benchmark-recovery-report benchmark-knowledge-evolution benchmark-autonomous-report benchmark-autonomous benchmark-build benchmark-manifest benchmark-report benchmark-full

doctor:
	sh scripts/doctor.sh

bootstrap:
	@command -v brew >/dev/null 2>&1 || { echo "Homebrew is required on macOS"; exit 1; }
	brew list minikube >/dev/null 2>&1 || brew install minikube
	brew list kubernetes-cli >/dev/null 2>&1 || brew install kubernetes-cli
	brew list helm >/dev/null 2>&1 || brew install helm
	@test -f .env || cp .env.example .env
	chmod +x scripts/*.sh

cluster-up:
	colima status >/dev/null 2>&1 || colima start --cpu 8 --memory 22 --disk 100
	minikube status >/dev/null 2>&1 || minikube start --driver=docker --container-runtime=containerd --cpus=7 --memory=18000mb --cni=calico

runtime:
	sh scripts/generate-runtime.sh

infra-up: runtime
	$(COMPOSE) up -d --build postgres agent-redis drain3 prometheus alertmanager loki jaeger etcd minio milvus grafana

demo-up:
	docker build -f deploy/kubernetes/Dockerfile.demo -t kubepilot/demo-service:0.1.0 .
	minikube image load kubepilot/demo-service:0.1.0
	kubectl apply -k deploy/kubernetes/overlays/demo
	kubectl apply -k deploy/kubernetes/overlays/benchmark
	kubectl apply -f deploy/kubernetes/observability/alloy.yaml
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts >/dev/null 2>&1 || true
	helm repo update prometheus-community
	helm upgrade --install kube-state-metrics prometheus-community/kube-state-metrics --namespace kubepilot-system --create-namespace --version 8.1.2
	kubectl rollout status deployment/gateway-service -n kubepilot-demo --timeout=180s
	kubectl rollout status deployment/gateway-service -n kubepilot-benchmark --timeout=180s

up: cluster-up infra-up demo-up runtime
	$(COMPOSE) up -d --build log-indexer kubepilot-agent

down:
	$(COMPOSE) down

destroy:
	@printf 'Delete KubePilot Minikube cluster and all Compose volumes? [y/N] '; read answer; [ "$$answer" = y ]
	$(COMPOSE) down -v
	minikube delete

migrate:
	$(COMPOSE) exec -T postgres sh -c 'for migration in /docker-entrypoint-initdb.d/*.sql; do psql -v ON_ERROR_STOP=1 -U kubepilot -d kubepilot -f "$$migration"; done'

test:
	go test -race ./...

lint:
	go vet ./...
	$(COMPOSE) config --quiet
	kubectl kustomize deploy/kubernetes/overlays/demo >/dev/null
	kubectl kustomize deploy/kubernetes/overlays/benchmark >/dev/null

benchmark-validate:
	go run ./cmd/benchmark validate

benchmark-history-seed:
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark seed-history --milvus-url localhost:19530 --namespaces "kubepilot-benchmark,$${BENCHMARK_WORKER_NAMESPACES:-$(BENCHMARK_WORKER_NAMESPACES)}" --output "$(BENCHMARK_ARTIFACT_DIR)/history-seed/manifest.json"

benchmark-smoke: benchmark-reset-diagnosis
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark run --profile smoke --kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" --token "$${API_TOKEN}"

benchmark-reset-diagnosis:
	bash scripts/reset-diagnosis-benchmark.sh

benchmark-standard: benchmark-reset-diagnosis
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark run --profile standard --run-id "$(BENCHMARK_RUN_ID)" --artifact-dir "$(BENCHMARK_ARTIFACT_DIR)/diagnosis" --kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" --token "$${API_TOKEN}" --dataset-split test --seeds 20260803,20260804,20260805 --repetitions 1 --compare-methods --strategies direct,rag,react,kubepilot --workers "$(BENCHMARK_WORKERS)" --model-concurrency "$(BENCHMARK_MODEL_CONCURRENCY)" --worker-namespaces "$${BENCHMARK_WORKER_NAMESPACES:-$(BENCHMARK_WORKER_NAMESPACES)}" --auto-approve

benchmark-causal-ablation: benchmark-standard
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark run --profile standard --run-id "$(BENCHMARK_RUN_ID)-causal-ablation" --artifact-dir "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/no-causal" --kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" --token "$${API_TOKEN}" --dataset-split test --seeds 20260803,20260804,20260805 --repetitions 1 --diagnosis-method kubepilot --causal-mode no-causal --workers "$(BENCHMARK_WORKERS)" --model-concurrency "$(BENCHMARK_MODEL_CONCURRENCY)" --worker-namespaces "$${BENCHMARK_WORKER_NAMESPACES:-$(BENCHMARK_WORKER_NAMESPACES)}" --auto-approve
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark run --profile standard --run-id "$(BENCHMARK_RUN_ID)-causal-ablation" --artifact-dir "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/static-causal" --kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" --token "$${API_TOKEN}" --dataset-split test --seeds 20260803,20260804,20260805 --repetitions 1 --diagnosis-method kubepilot --causal-mode static-causal --workers "$(BENCHMARK_WORKERS)" --model-concurrency "$(BENCHMARK_MODEL_CONCURRENCY)" --worker-namespaces "$${BENCHMARK_WORKER_NAMESPACES:-$(BENCHMARK_WORKER_NAMESPACES)}" --auto-approve
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark run --profile standard --run-id "$(BENCHMARK_RUN_ID)-causal-ablation" --artifact-dir "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/learned-causal" --kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" --token "$${API_TOKEN}" --dataset-split test --seeds 20260803,20260804,20260805 --repetitions 1 --diagnosis-method kubepilot --causal-mode learned-causal --workers "$(BENCHMARK_WORKERS)" --model-concurrency "$(BENCHMARK_MODEL_CONCURRENCY)" --worker-namespaces "$${BENCHMARK_WORKER_NAMESPACES:-$(BENCHMARK_WORKER_NAMESPACES)}" --auto-approve
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark run --profile standard --run-id "$(BENCHMARK_RUN_ID)-causal-ablation" --artifact-dir "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/full" --kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" --token "$${API_TOKEN}" --dataset-split test --seeds 20260803,20260804,20260805 --repetitions 1 --diagnosis-method kubepilot --causal-mode full --workers "$(BENCHMARK_WORKERS)" --model-concurrency "$(BENCHMARK_MODEL_CONCURRENCY)" --worker-namespaces "$${BENCHMARK_WORKER_NAMESPACES:-$(BENCHMARK_WORKER_NAMESPACES)}" --auto-approve

benchmark-causal-ablation-report: benchmark-causal-ablation
	go run ./cmd/benchmark causal-ablation-report --run-id "$(BENCHMARK_RUN_ID)-causal-ablation" --no-causal "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/no-causal/cases.jsonl" --static-causal "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/static-causal/cases.jsonl" --learned-causal "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/learned-causal/cases.jsonl" --full "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/full/cases.jsonl" --output "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/report"

benchmark-correlation: benchmark-reset-diagnosis
	set -a; [ ! -f .env ] || . ./.env; set +a; mkdir -p "$(BENCHMARK_ARTIFACT_DIR)/correlation"; go run ./cmd/benchmark correlation --agent-url http://localhost:8080 --webhook-token "$${ALERTMANAGER_WEBHOOK_TOKEN}" --output "$(BENCHMARK_ARTIFACT_DIR)/correlation/correlation-summary.json"

benchmark-build: runtime
	go test ./...
	go vet ./...
	go build ./cmd/server ./cmd/benchmark
	docker build -f deploy/kubernetes/Dockerfile.demo -t kubepilot/demo-service:0.1.0 .
	minikube image load kubepilot/demo-service:0.1.0
	kubectl apply -k deploy/kubernetes/overlays/benchmark
	kubectl apply -f deploy/kubernetes/observability/alloy.yaml
	kubectl rollout restart daemonset/alloy -n kubepilot-system
	kubectl rollout status daemonset/alloy -n kubepilot-system --timeout=180s
	$(COMPOSE) up -d --build --force-recreate kubepilot-agent
	@for attempt in $$(seq 1 60); do curl -fsS http://localhost:8080/readyz >/dev/null && break; [ $$attempt -lt 60 ] || exit 1; sleep 2; done

benchmark-manifest: benchmark-build
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark environment --manifest benchmark/manifests/default.yaml --output artifacts/benchmark/manifest/runtime.json

benchmark-component: benchmark-manifest benchmark-history-seed
	go test ./benchmark/evaluator/... ./benchmark/log_retrieval/... ./benchmark/incident_retrieval/...

benchmark-log-retrieval: benchmark-component
	set -a; [ ! -f .env ] || . ./.env; set +a; docker compose -p kubepilot-retrieval -f deploy/docker/docker-compose.yml --profile benchmark down -v --remove-orphans; docker compose -p kubepilot-retrieval -f deploy/docker/docker-compose.yml --profile benchmark up -d --force-recreate --wait drain3-benchmark loki-benchmark; for attempt in $$(seq 1 60); do curl -fsS http://localhost:3200/ready >/dev/null && break; [ $$attempt -lt 60 ] || exit 1; sleep 2; done; go run ./cmd/benchmark log-retrieval --run-id "$(BENCHMARK_RUN_ID)" --output "$(BENCHMARK_ARTIFACT_DIR)/log-retrieval" --count "$${LOG_RETRIEVAL_RECORDS:-500000}" --loki-url http://localhost:3200 --drain3-url ws://localhost:8181/ws/v1/parse

benchmark-incident-retrieval: benchmark-log-retrieval
	set -a; [ ! -f .env ] || . ./.env; set +a; $(COMPOSE) exec -T kubepilot-agent /usr/local/bin/kubepilot-benchmark incident-retrieval --output "/app/$(BENCHMARK_ARTIFACT_DIR)/incident-retrieval" --count "$${INCIDENT_RETRIEVAL_QUERIES:-500}"

benchmark-agent: benchmark-incident-retrieval benchmark-standard

benchmark-agent-report: benchmark-agent
	go run ./cmd/benchmark agent-report --input "$(BENCHMARK_ARTIFACT_DIR)/diagnosis/kubepilot/cases.jsonl" --output "$(BENCHMARK_ARTIFACT_DIR)/agent/agent_behavior_report.json"

benchmark-recovery-report: benchmark-agent-report
	go run ./cmd/benchmark recovery-report --input "$(BENCHMARK_ARTIFACT_DIR)/diagnosis/kubepilot/cases.jsonl" --output "$(BENCHMARK_ARTIFACT_DIR)/recovery/recovery_report.json" --count "$${RECOVERY_CASES:-50}"

benchmark-knowledge-evolution: benchmark-recovery-report benchmark-causal-ablation-report
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark intelligence --output "$(BENCHMARK_ARTIFACT_DIR)/knowledge-evolution/summary.json"

benchmark-autonomous-report: benchmark-knowledge-evolution benchmark-correlation
	go run ./cmd/benchmark autonomous-report --diagnosis "$(BENCHMARK_ARTIFACT_DIR)/diagnosis/diagnosis-comparison.json" --agent "$(BENCHMARK_ARTIFACT_DIR)/agent/agent_behavior_report.json" --recovery "$(BENCHMARK_ARTIFACT_DIR)/recovery/recovery_report.json" --knowledge "$(BENCHMARK_ARTIFACT_DIR)/knowledge-evolution/summary.json" --ablation "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/report/causal-ablation.json" --log-retrieval "$(BENCHMARK_ARTIFACT_DIR)/log-retrieval/log_retrieval_report.json" --incident-retrieval "$(BENCHMARK_ARTIFACT_DIR)/incident-retrieval/incident_retrieval_report.json" --output "$(BENCHMARK_ARTIFACT_DIR)/autonomous/autonomous_sre_report.json"

benchmark-autonomous: benchmark-autonomous-report

benchmark-report: benchmark-autonomous
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark suite-report --manifest benchmark/manifests/default.yaml --root artifacts/benchmark --diagnosis "$(BENCHMARK_ARTIFACT_DIR)/diagnosis/diagnosis-comparison.json" --agent "$(BENCHMARK_ARTIFACT_DIR)/agent/agent_behavior_report.json" --recovery "$(BENCHMARK_ARTIFACT_DIR)/recovery/recovery_report.json" --knowledge "$(BENCHMARK_ARTIFACT_DIR)/knowledge-evolution/summary.json" --ablation "$(BENCHMARK_ARTIFACT_DIR)/causal-ablation/report/causal-ablation.json" --correlation "$(BENCHMARK_ARTIFACT_DIR)/correlation/correlation-summary.json" --autonomous "$(BENCHMARK_ARTIFACT_DIR)/autonomous/autonomous_sre_report.json" --log-retrieval "$(BENCHMARK_ARTIFACT_DIR)/log-retrieval/log_retrieval_report.json" --incident-retrieval "$(BENCHMARK_ARTIFACT_DIR)/incident-retrieval/incident_retrieval_report.json" --output "$(BENCHMARK_ARTIFACT_DIR)/report"

benchmark-full: benchmark-report
