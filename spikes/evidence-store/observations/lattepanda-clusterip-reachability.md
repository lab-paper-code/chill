# Lattepanda ClusterIP Reachability

## Scope

Observed on 2026-07-15 from a one-shot Pod scheduled to the `lattepanda`
KubeEdge Node. The probe had no Kubernetes API token, no evidence-store
credential, and performed no S3 operation.

Target state at observation time:

- Service: `chill-system/chill-evidence-store-spike`
- Service type: `ClusterIP`
- ClusterIP: `10.96.3.130:9000`
- Service FQDN:
  `chill-evidence-store-spike.chill-system.svc.cluster.local:9000`
- backing Pod Node: `genesis2`
- probe Job: `chill-evidence-store-clusterip-probe-hkpjn`
- EdgeMesh opt-in label:
  `service.edgemesh.kubeedge.io/service-proxy-name: ""`

## Result

Both internal targets returned HTTP 200 in all five attempts:

| Attempt | ClusterIP | Service FQDN |
|---:|---:|---:|
| 1 | 103.9 ms | 11.0 ms |
| 2 | 5.7 ms | 7.2 ms |
| 3 | 5.9 ms | 5.3 ms |
| 4 | 6.6 ms | 6.3 ms |
| 5 | 5.5 ms | 6.4 ms |

The Job completed successfully with exit code 0 on Lattepanda.

## Failure cause established before the successful run

Without the EdgeMesh opt-in label, the direct ClusterIP timed out because the
Service was not registered with the edge proxy. After the label was applied,
the same ClusterIP reached the cloud-side evidence-store Pod through EdgeMesh.

The initial DNS probe used the incomplete name ending in `.svc`; EdgeMesh DNS
correctly logged NXDOMAIN for that absolute query. Using the Kubernetes Service
FQDN ending in `.svc.cluster.local` resolved and connected successfully.

## Boundary established

CHILL edge workers can use the stable internal endpoint
`http://chill-evidence-store-spike.chill-system.svc.cluster.local:9000` in the
current testbed. They do not need a Node IP, NodePort, Service discovery through
the Kubernetes API, or a mounted API token.

The CHILL-managed Service must retain the EdgeMesh opt-in label. This label is
part of the deployment integration for the current KubeEdge testbed, while the
worker-facing contract remains an ordinary Kubernetes Service FQDN.
