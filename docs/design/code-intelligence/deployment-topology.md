# Deployment Topology Review

## Decision

KubeDiagrams v0.8.0 is used only to visualize validated Helm desired state. The
rendered manifest and sorted resource set are authoritative; SVG and DOT JSON
are review views. Generation is local and absent from correctness CI.

## Canonical profiles

| Profile | Helm input |
|---|---|
| `default` | chart defaults |
| `profiler` | chart defaults plus `profiler.enabled=true` and a synthetic observer Node |

Profiles are rendered and stored separately. A combined graph must not be
described as one deployable state.

## 외부 EdgeMesh 배포 선언

EdgeMesh는 CHILL chart 밖의 `kubeedge/edgemesh` release다. CHILL main 병합과 최종 배포에서도
다음 기존 선언을 보존하며, CHILL의 default/profiler render에 합치거나 재배포하지 않는다.

| 항목 | 보존할 값 |
|---|---|
| DaemonSet | `kubeedge/edgemesh-agent` |
| registry image | `155.230.35.213:5000/kubeedge/edgemesh-agent:udpfix-allnodes-20260219` |
| 고정 index | `sha256:bb1e6ad0ef80cb0d477c174a9e264f8cfba533da9775b70a7d550ef5185271f9` |
| container limits | CPU `2`, memory `1Gi` |
| container requests | CPU `500m`, memory `128Mi` |
| updateStrategy | `OnDelete` |

선언 근거는 `/home/genesis1/.cache/chill-assets/ops/edgemesh-bump/dryrun-registry.yaml`의
image/resources와 같은 디렉터리 `ds-before.yaml`의 `OnDelete`다.
실제 적용 근거는 `/home/genesis1/.cache/chill-assets/ops/pools-deploy/deploy-log.md`의
2026-10-25 mesh 재개 기록이다. 이 기록은 승인된 index와 자원 한도로 10대 교체를 마친
05:45:25.495223Z snapshot을 남긴다. dry-run 자체를 적용 증거로 쓰지 않는다.
옛 rev 12 값이나 만료되는 `ttl.sh` image로 되돌리지 않으며, 외부 release의 재배포·Pod 교체는
별도 승인 범위다. 이 문서 수정에서는 live 상태를 새로 조회하거나 변경하지 않았다.

## Commands

```bash
make deployment-graph-default
make deployment-graph-profiler
make deployment-graph-check
make code-intelligence-review
```

`deployment-graph-check` regenerates both profiles and verifies:

- kubeconform accepts both manifests;
- the Profiler profile adds the reviewed fifteen-resource set;
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
