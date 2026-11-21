# Crashbox

**A flight recorder for dying pods.**

When a container hits `CrashLoopBackOff` or is `OOMKilled`, the evidence you
need (the logs of the container that died, the events that led up to it) is
gone after the next restart or event TTL. Crashbox watches for those moments
and immediately captures a structured bundle to local disk, S3 or GCS.

```
pod update ──▶ detect ──▶ dedupe/cooldown ──▶ collect ──────────────▶ sinks
               (CrashLoopBackOff,            previous + current logs   file:// (PVC)
                OOMKilled)                   events, pod, describe     s3://
                                             metrics, node             gs://
```

## What's in a bundle

`manifest.json` (machine-readable index), `pod.json`, `describe.txt`,
`events.json`, `logs/<container>/previous.log`, `logs/<container>/current.log`,
`metrics/pod.json` and `node.json`. See [docs/bundle-format.md](docs/bundle-format.md)
and the [JSON Schema](schema/manifest.schema.json).

Bundles are stored at `<cluster>/<namespace>/<pod>/<id>.tar.gz` with a sibling
`<id>.manifest.json`, so tools like K-Agent can list and filter crashes without
downloading archives.

## Run it

In-cluster (edit `CRASHBOX_SINK` in the ConfigMap first):

```sh
make image IMAGE=ghcr.io/you/crashbox:dev && docker push ghcr.io/you/crashbox:dev
kubectl apply -f deploy/crashbox.yaml
```

From a laptop, against your current kube context:

```sh
go run ./cmd/crashbox watch --sink ./bundles                 # watch everything
go run ./cmd/crashbox watch -namespace shop --sink s3://my-bucket/crashbox?region=us-east-1
go run ./cmd/crashbox capture -n shop api-7d9f-x2 --sink ./bundles   # one pod, now
```

### Flags (watch)

| Flag | Default | Meaning |
|---|---|---|
| `--sink` | `$CRASHBOX_SINK` or `./crashbox-bundles` | path, `file://`, `s3://bucket/prefix`, `gs://bucket/prefix`; repeatable |
| `--cluster` | `$CRASHBOX_CLUSTER` | name used in bundle keys |
| `--namespace` / `--selector` | all | limit what's watched |
| `--cooldown` | `15m` | min gap between bundles for the same container |
| `--max-age` | `10m` | ignore OOM terminations older than this at startup |
| `--log-tail-lines` / `--log-max-bytes` | `5000` / `10MiB` | per-log limits |
| `--current-logs` | `true` | also grab the restarted container's logs |
| `--no-metrics` | `false` | skip metrics.k8s.io |
| `--workers` / `--capture-timeout` | `4` / `60s` | capture concurrency |
| `--health-addr` | `$CRASHBOX_HEALTH_ADDR` or `:9808` | `/healthz` listener; empty disables. A bind failure is a warning, not fatal |

S3 also accepts `?endpoint=http://minio:9000&pathStyle=true` for S3-compatible
stores. Credentials come from the standard AWS / Google chains, so IRSA, EKS Pod
Identity and GKE Workload Identity work without extra config (annotate the
ServiceAccount in `deploy/crashbox.yaml`).

## Permissions

Read-only: `get/list/watch` on pods, pods/log, events, nodes, plus `get` on
`metrics.k8s.io` pods and nodes. Crashbox never changes the cluster.

## How detection works

* **CrashLoopBackOff**: a container whose current state is `Waiting` with that reason.
* **OOMKilled**: a container whose current or last termination reason is `OOMKilled`.
  This catches OOMs even when the container restarts straight back to `Running`.
* One bundle per pod update, covering every container that triggered.
* Deduped by pod UID + container + restart count, with a cooldown so a pod
  crashlooping for hours doesn't produce hundreds of bundles.

## Develop

```sh
make test   # unit tests (fake clientset)
make build
```
