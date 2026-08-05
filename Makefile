SHELL := /bin/sh
COMPOSE := docker compose -f deploy/docker/docker-compose.yml

.PHONY: doctor bootstrap cluster-up infra-up demo-up runtime up down destroy test lint migrate benchmark-validate benchmark-history-seed benchmark-smoke benchmark-reset-diagnosis benchmark-standard benchmark-correlation benchmark-log-retrieval benchmark-incident-retrieval benchmark-component benchmark-agent benchmark-agent-report benchmark-recovery-report benchmark-knowledge-evolution benchmark-autonomous-report benchmark-autonomous benchmark-build benchmark-manifest benchmark-report benchmark-full

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
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark seed-history --milvus-url localhost:19530

benchmark-smoke: benchmark-reset-diagnosis
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark run --profile smoke --kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" --token "$${API_TOKEN}"

benchmark-reset-diagnosis:
	bash scripts/reset-diagnosis-benchmark.sh

benchmark-standard: benchmark-reset-diagnosis
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark run --profile standard --kubeconfig "$${KUBECONFIG:-$$HOME/.kube/config}" --token "$${API_TOKEN}" --auto-approve

benchmark-correlation: benchmark-reset-diagnosis
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark correlation --agent-url http://localhost:8080 --webhook-token "$${ALERTMANAGER_WEBHOOK_TOKEN}"

benchmark-build:
	go test ./...
	go vet ./...
	go build ./cmd/server ./cmd/benchmark
	$(COMPOSE) up -d --build --force-recreate kubepilot-agent
	@for attempt in $$(seq 1 60); do curl -fsS http://localhost:8080/readyz >/dev/null && break; [ $$attempt -lt 60 ] || exit 1; sleep 2; done

benchmark-manifest: benchmark-build
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark environment --manifest benchmark/manifests/autonomous.yaml --output artifacts/benchmark/manifest/runtime.json

benchmark-component: benchmark-manifest
	go test ./benchmark/evaluator/... ./benchmark/log_retrieval/... ./benchmark/incident_retrieval/...

benchmark-log-retrieval: benchmark-component
	set -a; [ ! -f .env ] || . ./.env; set +a; docker compose -p kubepilot-retrieval -f deploy/docker/docker-compose.yml --profile benchmark down -v --remove-orphans; docker compose -p kubepilot-retrieval -f deploy/docker/docker-compose.yml --profile benchmark up -d --force-recreate --wait drain3-benchmark loki-benchmark; for attempt in $$(seq 1 60); do curl -fsS http://localhost:3200/ready >/dev/null && break; [ $$attempt -lt 60 ] || exit 1; sleep 2; done; go run ./cmd/benchmark log-retrieval --count "$${LOG_RETRIEVAL_RECORDS:-500000}" --loki-url http://localhost:3200 --drain3-url ws://localhost:8181/ws/v1/parse

benchmark-incident-retrieval: benchmark-log-retrieval
	set -a; [ ! -f .env ] || . ./.env; set +a; $(COMPOSE) exec -T kubepilot-agent /usr/local/bin/kubepilot-benchmark incident-retrieval --count "$${INCIDENT_RETRIEVAL_QUERIES:-500}"

benchmark-agent: benchmark-incident-retrieval benchmark-standard

benchmark-agent-report: benchmark-agent
	latest=$$(find artifacts/benchmark -type f -name cases.jsonl -print | sort | tail -1); test -n "$$latest"; go run ./cmd/benchmark agent-report --input "$$latest"

benchmark-recovery-report: benchmark-agent-report
	latest=$$(find artifacts/benchmark -type f -name cases.jsonl -print | sort | tail -1); test -n "$$latest"; go run ./cmd/benchmark recovery-report --input "$$latest" --count "$${RECOVERY_CASES:-50}"

benchmark-knowledge-evolution: benchmark-recovery-report
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark intelligence

benchmark-autonomous-report: benchmark-knowledge-evolution benchmark-correlation
	latest=$$(find artifacts/benchmark -type f -name cases.jsonl -print | sort | tail -1); test -n "$$latest"; diagnosis_dir=$$(dirname "$$latest"); go run ./cmd/benchmark autonomous-report --diagnosis "$$diagnosis_dir/summary.json"

benchmark-autonomous: benchmark-autonomous-report

benchmark-report: benchmark-autonomous
	set -a; [ ! -f .env ] || . ./.env; set +a; go run ./cmd/benchmark suite-report --manifest benchmark/manifests/autonomous.yaml --root artifacts/benchmark

benchmark-full: benchmark-report
