# Crashbox bundle format (`crashbox.dev/v1alpha1`)

A bundle is everything Crashbox could capture about one pod at the moment one
or more of its containers crashed. It is designed to be read by machines first
(K-Agent, scripts, search indexes) and by people second.

## Storage layout

Every bundle is stored as two objects in each configured sink:

```
<prefix>/<cluster>/<namespace>/<pod>/<id>.tar.gz          # the evidence
<prefix>/<cluster>/<namespace>/<pod>/<id>.manifest.json   # the index, written last
```

* `id` is `<UTC timestamp>-<pod>-<first 8 chars of pod UID>`, e.g.
  `20261121T060000Z-api-7d9f-x2-1234abcd`. IDs sort by capture time.
* The manifest is written **after** the archive, so a manifest's presence means
  the bundle is complete. Consumers should list `*.manifest.json` and only
  download the archive when they need the raw evidence.
* Grouping by pod keeps a crashlooping pod's history together.

## Archive contents

```
<id>/manifest.json                 same document as <id>.manifest.json
<id>/pod.json                      full Pod object (managedFields stripped)
<id>/describe.txt                  kubectl-describe-style summary
<id>/events.json                   Events for the pod, oldest first
<id>/logs/<container>/previous.log logs of the container that died (timestamps on)
<id>/logs/<container>/current.log  logs of the restarted container (if enabled)
<id>/metrics/pod.json              PodMetrics from metrics.k8s.io (if available)
<id>/node.json                     node conditions, capacity, allocatable, usage
```

Never rely on paths alone: use `manifest.files[].type`, which is one of
`pod`, `describe`, `events`, `logs/previous`, `logs/current`, `metrics/pod`, `node`.

## Manifest

The JSON Schema lives in [`schema/manifest.schema.json`](../schema/manifest.schema.json).

```json
{
  "apiVersion": "crashbox.dev/v1alpha1",
  "kind": "CrashBundle",
  "id": "20261121T060000Z-api-7d9f-x2-aaaabbbb",
  "capturedAt": "2026-11-21T06:00:00Z",
  "cluster": "prod",
  "collector": {"name": "crashbox", "version": "0.1.0"},
  "pod": {
    "namespace": "shop", "name": "api-7d9f-x2", "uid": "aaaa-bbbb-cccc",
    "node": "node-1", "phase": "Running", "qosClass": "Burstable",
    "labels": {"app": "api"},
    "owner":    {"kind": "ReplicaSet", "name": "api-7d9f"},
    "workload": {"kind": "Deployment", "name": "api"}
  },
  "triggers": [{
    "reason": "OOMKilled", "container": "app", "init": false, "restartCount": 3,
    "exitCode": 137, "terminationReason": "OOMKilled",
    "terminatedAt": "2026-11-21T05:59:41Z"
  }],
  "containers": [{
    "name": "app", "init": false, "image": "api:1.2", "ready": false, "restartCount": 3,
    "state": "Waiting: CrashLoopBackOff", "lastState": "Terminated: OOMKilled (exit 137)",
    "limits": {"memory": "128Mi"}, "usage": {"cpu": "1m", "memory": "3Mi"}
  }],
  "files": [
    {"path": "logs/app/previous.log", "type": "logs/previous", "container": "app",
     "bytes": 18234, "sha256": "…", "truncated": false}
  ],
  "errors": [
    {"collector": "metrics/pod", "message": "the server could not find the requested resource"}
  ]
}
```

### Field notes

* **`triggers[].reason`** is `OOMKilled`, `CrashLoopBackOff`, or `Manual` (from
  `crashbox capture`). When a container is both crashlooping and was OOM
  killed, the reason is `OOMKilled` because it is the more specific diagnosis.
* **`pod.workload`** is the top-level controller. A ReplicaSet owner is mapped
  to its Deployment using the `pod-template-hash` label. This is the field to
  group by when asking "which service keeps crashing?".
* **`containers[].usage`** is a sample taken *after* the crash, so for the dead
  container it usually reflects the restarted process, not the peak. Compare
  `limits` with `triggers[].reason` rather than trusting usage for OOMs.
* **`errors`** lists collectors that failed. A bundle with errors is still
  valid: partial evidence beats none.
* **`files[].truncated`** is true when a log hit `--log-max-bytes`.

## Compatibility

`v1alpha1` may change. Additive fields will not bump the version; renames or
removals will move to `v1alpha2`, and consumers should check `apiVersion`.
