# Deployment Topology Review

KubeDiagrams v0.8.0 visualizes validated Helm desired state. Rendered manifests
and sorted resources are authoritative; SVG/DOT JSON are local review views.
Graph generation is outside correctness CI.

## Canonical profiles

| Profile | Helm input |
|---|---|
| `default` | Chart defaults |
| `profiler` | Defaults plus `profiler.enabled=true` and synthetic observer Node |

Render/store profiles separately. A combined graph is not one deployable state.
Custom values require a separately named profile with recorded inputs.

EdgeMesh is an external release. CHILL renders do not include or redeploy it.
Changing its image/resources or replacing Pods requires separate approval and
current deployment evidence.

## Commands and checks

```bash
make deployment-graph-default
make deployment-graph-profiler
make deployment-graph-check
make code-intelligence-review
```

The check regenerates both profiles, validates manifests with kubeconform and
compares Profiler-only resources to
[`profiler-only-resources.txt`](../../../hack/testdata/deployment-graph/profiler-only-resources.txt).
It checks ServiceAccount/binding relationships, ChillSystem ConfigMap references,
metadata inputs and absence of Helm-owned `Deployment/chill-profiler`.
The Operator owns that operand.

## Artifacts and isolation

Ignored output in `bin/code-intelligence/deployment/<profile>/` contains
`manifest.yaml`, `resources.txt`, `diagram.svg`, `diagram.dot_json` and
`metadata.json`. Metadata records source/chart state, chart/app versions,
release/namespace, exact values, tool versions, digest, hashes and resource count.

The Makefile digest-pins KubeDiagrams. It runs without network, with read-only
root, temporary `/tmp`, writable output and read-only relationship configuration.
Ordinary CI does not upload/commit generated views.

## Interpretation limits

- Desired state does not prove live resources, readiness, traffic or reconciliation.
- A resource graph does not establish minimum RBAC privilege.
- Operator operands absent from Helm remain absent from the graph; state ownership.
- Diagram layout/Node placement is not a correctness signal.
- Keep validated manifests/resources if visualization regresses.
