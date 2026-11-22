#!/usr/bin/env bash
# End-to-end check on a real cluster (kind in CI). Starts the watcher, breaks
# pods in the two ways Crashbox cares about, and asserts on the bundles.
set -euo pipefail

OUT=${OUT:-$PWD/e2e-bundles}
NS=crashbox-e2e
TIMEOUT=${TIMEOUT:-240}
rm -rf "$OUT"; mkdir -p "$OUT"

go build -o bin/crashbox ./cmd/crashbox

# metrics-server is optional; Crashbox must cope either way.
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml >/dev/null
kubectl -n kube-system patch deploy metrics-server --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]' >/dev/null

# Give metrics-server time to serve, so the usage path is exercised too.
kubectl -n kube-system rollout status deploy/metrics-server --timeout=180s >/dev/null
for _ in $(seq 90); do kubectl top node >/dev/null 2>&1 && break; sleep 2; done

./bin/crashbox watch --cluster e2e --sink "$OUT" --cooldown 0s --health-addr "" > "$OUT/watcher.log" 2>&1 &
WATCHER=$!
trap 'rc=$?; kill $WATCHER 2>/dev/null || true; if [[ $rc != 0 ]]; then echo "--- e2e failed (exit $rc), watcher log:"; cat "$OUT/watcher.log"; fi' EXIT
until grep -q '"watching pods"' "$OUT/watcher.log"; do sleep 1; done

kubectl create ns "$NS"
kubectl -n "$NS" create deployment crasher --image=busybox:1.36 -- sh -c 'echo "about to die"; sleep 3; exit 3'
kubectl -n "$NS" apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata: {name: oomer}
spec:
  containers:
  - name: hog
    image: polinux/stress
    command: [stress, --vm, "1", --vm-bytes, 250M, --vm-hang, "1"]
    resources: {limits: {memory: 64Mi}}
---
apiVersion: v1
kind: Pod
metadata: {name: healthy}
spec:
  containers:
  - name: ok
    image: busybox:1.36
    command: [sh, -c, 'echo fine; sleep 3600']
YAML

manifest() { find "$OUT/e2e/$NS" -path "*$1*" -name '*.manifest.json' 2>/dev/null | sort | head -1; }

echo "waiting up to ${TIMEOUT}s for bundles..."
for _ in $(seq "$TIMEOUT"); do
  [[ -n "$(manifest crasher)" && -n "$(manifest oomer)" ]] && break
  sleep 1
done
kubectl -n "$NS" get pods -o wide

fail=0
check() { if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; fail=1; fi; }

C=$(manifest crasher); O=$(manifest oomer)
check "crasher bundle captured"            '[[ -n "$C" ]]'
check "oomer bundle captured"              '[[ -n "$O" ]]'
if [[ -n "$C" ]]; then
  CT=${C%.manifest.json}.tar.gz
  check "crasher trigger is CrashLoopBackOff" '[[ $(jq -r ".triggers[0].reason" "$C") == CrashLoopBackOff ]]'
  check "crasher exit code is 3"              '[[ $(jq -r ".triggers[0].exitCode" "$C") == 3 ]]'
  check "crasher workload is the Deployment"  '[[ $(jq -r ".pod.workload.kind+\"/\"+.pod.workload.name" "$C") == Deployment/crasher ]]'
  check "previous log has the dying words"    'tar -xzOf "$CT" --wildcards "*/logs/*/previous.log" | grep -q "about to die"'
  check "events captured"                     '[[ $(tar -xzOf "$CT" --wildcards "*/events.json" | jq length) -gt 0 ]]'
  check "describe.txt mentions BackOff"       'tar -xzOf "$CT" --wildcards "*/describe.txt" | grep -q BackOff'
  check "node.json captured with usage"      '[[ $(tar -xzOf "$CT" --wildcards "*/node.json" | jq -r ".usage.memory // empty") != "" ]]'
  check "every file checksum matches"         'jq -r ".files[]|.sha256+\"  \"+.path" "$C" > /tmp/sums && d=$(mktemp -d) && tar -xzf "$CT" -C "$d" && (cd "$d"/* && sha256sum -c --quiet /tmp/sums)'
fi
if [[ -n "$O" ]]; then
  check "oomer trigger is OOMKilled"          '[[ $(jq -r ".triggers[0].reason" "$O") == OOMKilled ]]'
  check "oomer limit recorded as 64Mi"        '[[ $(jq -r ".containers[0].limits.memory" "$O") == 64Mi ]]'
  check "oomer dead container's log captured" '[[ $(jq "[.files[]|select(.type==\"logs/previous\")]|length" "$O") == 1 ]]'
  check "oomer has no log collection errors"  '[[ $(jq "[.errors[]?|select(.collector|startswith(\"logs/\"))]|length" "$O") == 0 ]]'
fi
check "healthy pod left alone"               '[[ -z "$(manifest healthy)" ]]'

# One-shot capture of a pod that isn't crashing.
# Flags deliberately come after the pod name, kubectl-style.
./bin/crashbox capture healthy -n "$NS" --cluster e2e --sink "$OUT" 2>>"$OUT/watcher.log" || true
H=$(manifest healthy)
check "manual capture works"                 '[[ -n "$H" && $(jq -r ".triggers[0].reason" "$H") == Manual ]]'
check "watcher logged no errors"             '! grep -q "\"level\":\"ERROR\"" "$OUT/watcher.log"'

echo "--- watcher log"; cat "$OUT/watcher.log"
[[ -n "$O" ]] && { echo "--- oomer manifest"; cat "$O"; }
[[ -n "$C" ]] && { echo "--- crasher previous.log"; tar -xzOf "${C%.manifest.json}.tar.gz" --wildcards "*/logs/*/previous.log"; }
exit $fail
