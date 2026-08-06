#!/bin/sh
set -eu
root_dir=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
runtime_dir="$root_dir/.runtime"
generated_dir="$root_dir/deploy/docker/prometheus/generated"
mkdir -p "$runtime_dir" "$generated_dir"

kubectl config view --minify --raw --flatten > "$runtime_dir/kubeconfig"
cluster_name=$(kubectl config view --minify -o jsonpath='{.clusters[0].name}')
server=$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')
host_server=$(printf '%s' "$server" | sed 's#127.0.0.1#host.docker.internal#')
KUBECONFIG="$runtime_dir/kubeconfig" kubectl config set-cluster "$cluster_name" --server="$host_server" --insecure-skip-tls-verify=true >/dev/null

cert_data=$(kubectl config view --minify --raw -o jsonpath='{.users[0].user.client-certificate-data}')
key_data=$(kubectl config view --minify --raw -o jsonpath='{.users[0].user.client-key-data}')
if [ -n "$cert_data" ] && [ -n "$key_data" ]; then
  printf '%s' "$cert_data" | base64 --decode > "$generated_dir/client.crt"
  printf '%s' "$key_data" | base64 --decode > "$generated_dir/client.key"
else
  cert_path=$(kubectl config view --minify --raw -o jsonpath='{.users[0].user.client-certificate}')
  key_path=$(kubectl config view --minify --raw -o jsonpath='{.users[0].user.client-key}')
  if [ ! -r "$cert_path" ] || [ ! -r "$key_path" ]; then
    echo "unable to resolve Kubernetes client certificate and key" >&2
    exit 1
  fi
  cp "$cert_path" "$generated_dir/client.crt"
  cp "$key_path" "$generated_dir/client.key"
fi
chmod 600 "$runtime_dir/kubeconfig" "$generated_dir/client.key"

api_port=$(printf '%s' "$server" | sed -E 's#https://[^:]+:([0-9]+)#\1#')
sed -e "s/host.docker.internal:8443/host.docker.internal:$api_port/" "$root_dir/deploy/docker/prometheus/minikube-targets.template.yml" > "$runtime_dir/minikube-targets.yml"
worker_namespaces=${BENCHMARK_WORKER_NAMESPACES:-kubepilot-benchmark-worker-01,kubepilot-benchmark-worker-02,kubepilot-benchmark-worker-03,kubepilot-benchmark-worker-04}
for namespace in $(printf '%s' "$worker_namespaces" | tr ',' ' '); do
  for service in gateway-service order-service payment-service; do
    {
      printf '%s\n' '- targets: ["host.docker.internal:'"$api_port"'"]'
      printf '%s\n' '  labels:'
      printf '    __metrics_path__: /api/v1/namespaces/%s/services/http:%s:http/proxy/metrics\n' "$namespace" "$service"
      printf '%s\n' '    cluster: kubepilot-local'
      printf '    namespace: %s\n' "$namespace"
      printf '    service: %s\n' "$service"
    } >> "$runtime_dir/minikube-targets.yml"
  done
done
cp "$runtime_dir/minikube-targets.yml" "$generated_dir/minikube-targets.yml"
echo "generated runtime kubeconfig and Prometheus credentials"
