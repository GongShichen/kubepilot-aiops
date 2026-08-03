#!/bin/sh
set -eu
missing=""
for tool in go docker colima minikube kubectl helm; do
  if ! command -v "$tool" >/dev/null 2>&1; then missing="$missing $tool"; fi
done
if [ -n "$missing" ]; then
  echo "missing tools:$missing"
  exit 1
fi
docker info >/dev/null 2>&1 || { echo "Docker/Colima is not running"; exit 1; }
echo "Go: $(go version)"
echo "Docker: $(docker version --format '{{.Client.Version}}')"
echo "Minikube: $(minikube version --short)"
echo "kubectl: $(kubectl version --client=true -o yaml | sed -n 's/.*gitVersion: /version: /p' | head -n 1)"
echo "Helm: $(helm version --short)"
