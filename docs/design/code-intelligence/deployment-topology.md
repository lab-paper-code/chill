# Deployment Topology Review

## Decision

KubeDiagrams v0.8.0 is used only to visualize validated Helm desired state. The
rendered manifest and sorted resource set are authoritative; SVG and DOT JSON
are review views. Generation is local and absent from correctness CI.

## Canonical profiles

| Profile | Helm input |
|---|---|
| `default` | chart defaults |
| `profiler` | chart defaults plus `profiler.enabled=true` |

Profiles are rendered and stored separately. A combined graph must not be
described as one deployable state.

## Commands

```bash
make deployment-graph-default
make deployment-graph-profiler
make deployment-graph-check
make code-intelligence-review
```

`deployment-graph-check` regenerates both profiles and verifies:

- kubeconform accepts both manifests;
- the Profiler profile adds the reviewed seven-resource set;
- the Profiler graph contains its ServiceAccount and Role/ClusterRole binding
  relationships;
- `ChillSystem` references the Profiler ConfigMap;
- Helm does not render `Deployment/chill-profiler`; and
- metadata records the exact render and tool inputs.

## Artifacts

Generated files are ignored under:

```text
bin/code-intelligence/deployment/<profile>/
├── manifest.yaml
├── resources.txt
├── diagram.svg
├── diagram.dot_json
└── metadata.json
```

Metadata records the Git commit and chart-dirty state, chart and app versions,
release, namespace, exact `--set` list, Helm version, kubeconform flags,
KubeDiagrams version and image digest, input hashes, and resource count.

## Tool isolation

- Image: [`docker.io/philippemerle/kubediagrams` v0.8.0](https://github.com/philippemerle/KubeDiagrams/releases/tag/v0.8.0),
  pinned in the Makefile by multi-architecture manifest digest.
- The container runs without network access, with a read-only root filesystem,
  a temporary `/tmp`, the output directory as its only writable bind, and the
  CHILL relationship config mounted read-only.
- Generated artifacts are never committed or uploaded by ordinary CI.

## Boundaries

- A diagram does not prove live resources, readiness, traffic, reconciliation,
  or minimum RBAC privilege.
- Operator-created operands absent from the Helm render remain absent from the
  graph. Their ownership must be stated explicitly rather than drawn as a Helm
  resource.
- Node placement is not a correctness signal and need not be byte-identical
  across tool versions.
- Custom values require a separately named profile and recorded input contract;
  they are not silently merged into either canonical profile.
