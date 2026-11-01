#!/usr/bin/env bash
# Calibration lab (PLAN §9.3): runs the reference service with an HPA in a
# local kind cluster, drives the spike in k6/spike.js against it, and
# compares what happened with ArchSim's simulation of design.json.
#
#   lab/run.sh          run the experiment (asks first; LAB_YES=1 skips)
#   lab/run.sh clean    delete the kind cluster
#
# Needs Docker, kubectl and Go. kind and k6 run from pinned versions
# (go run / docker image), nothing is installed. Everything runs inside
# Docker, so Docker's CPU/memory limit caps what the lab can use.
set -euo pipefail
cd "$(dirname "$0")"

CLUSTER=archsim-lab
KIND_VERSION=v0.24.0
K6_IMAGE=grafana/k6:0.54.0
METRICS_SERVER=https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml
IMAGE=archsim-lab-work:dev

kind() { go run "sigs.k8s.io/kind@$KIND_VERSION" "$@"; }
k() { kubectl --context "kind-$CLUSTER" "$@"; }

case "${1:-run}" in
  clean)
    kind delete cluster --name "$CLUSTER"
    exit 0
    ;;
  run) ;;
  *)
    echo "usage: $0 [run|clean]" >&2
    exit 2
    ;;
esac

for tool in docker kubectl go; do
  command -v "$tool" >/dev/null || { echo "missing $tool" >&2; exit 1; }
done
cpus=$(docker info --format '{{.NCPU}}')
mem_gib=$(( $(docker info --format '{{.MemTotal}}') / 1024 / 1024 / 1024 ))
cat <<INFO
This will:
  - create kind cluster "$CLUSTER" (one node) and install metrics-server
  - build and load the reference service image ($IMAGE)
  - run k6 for ~8 minutes: 30 req/s, spike to 120 req/s (pods use at most 6 x 0.25 CPU)
Docker currently has $cpus CPU / ~${mem_gib} GiB; the lab stays within that.
Delete everything afterwards with: $0 clean
INFO
if [ "${LAB_YES:-}" != 1 ]; then
  read -r -p "Continue? [y/N] " answer
  [ "$answer" = y ] || [ "$answer" = Y ] || exit 1
fi

mkdir -p results
rm -f results/k6.csv results/pods.csv results/sim.json

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --config k8s/kind.yaml
fi
docker build -t "$IMAGE" .
kind load docker-image "$IMAGE" --name "$CLUSTER"

k apply -f "$METRICS_SERVER"
# kind's kubelets serve self-signed certificates.
if ! k -n kube-system get deploy metrics-server -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q kubelet-insecure-tls; then
  k -n kube-system patch deploy metrics-server --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
fi
k -n kube-system rollout status deploy/metrics-server --timeout=180s

k apply -f k8s/app.yaml
k rollout status deploy/work --timeout=180s
# Start from the steady state ArchSim starts from: the HPA has metrics and
# runs one ready pod. After a previous run its 300 s scale-down
# stabilization can take a few minutes to get back there.
echo "waiting for the HPA to see CPU metrics and settle at 1 pod..."
until k get hpa work -o jsonpath='{.status.currentMetrics[0].resource.current.averageUtilization}' | grep -q '[0-9]' \
  && [ "$(k get deploy work -o jsonpath='{.spec.replicas}/{.status.readyReplicas}')" = 1/1 ]; do
  sleep 5
done

# Replica counts once per second, next to k6's per-request samples.
(
  while true; do
    echo "$(date +%s),$(k get deploy work -o jsonpath='{.status.readyReplicas},{.spec.replicas}')" >> results/pods.csv
    sleep 1
  done
) &
watcher=$!
trap 'kill $watcher 2>/dev/null || true' EXIT

docker run --rm --network kind --user "$(id -u):$(id -g)" \
  -v "$PWD/k6:/scripts:ro" -v "$PWD/results:/results" \
  -e TARGET="http://$CLUSTER-control-plane:30080/work" \
  "$K6_IMAGE" run --quiet --out csv=/results/k6.csv /scripts/spike.js
kill "$watcher"

(cd .. && go run ./engine/cmd/archsim simulate --json lab/design.json) > results/sim.json
(cd .. && go run ./lab/cmd/analyze lab/results) | tee results/report.txt
echo
echo "Report saved to lab/results/report.txt. Delete the cluster with: $0 clean"
