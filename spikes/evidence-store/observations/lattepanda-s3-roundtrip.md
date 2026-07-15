# Lattepanda Authenticated S3 Round Trip

## Scope

Observed on 2026-07-15 using the CHILL-managed evidence-store spike and its
internal Kubernetes Service FQDN:

`http://chill-evidence-store-spike.chill-system.svc.cluster.local:9000`

The input was the existing profiling evidence bundle:

- source file:
  `spikes/profilingrun/observations/raw/sha256-165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42.json`
- byte size: `389364`
- SHA-256:
  `165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42`

The fixture ConfigMap was only a way to deliver known test input to the edge
writer. It was not used as evidence persistence.

## Execution

1. `chill-evidence-store-bucket-setup-c6jdk` ran on `genesis2` and created the
   `chill-profiling-evidence` bucket.
2. `chill-evidence-store-put-fx4b7` ran on `lattepanda`, verified the fixture
   digest and size locally, and issued an authenticated conditional `PutObject`
   using `If-None-Match: *`.
3. `chill-evidence-store-verify-9td9m` ran independently on `genesis2`, issued
   `GetObject`, and calculated digest and size from the downloaded bytes.

Both client Jobs used AWS CLI 2.35.23 through the pinned internal image:

`155.230.35.213:5000/chill/spike-s3-client@sha256:e73ec12f2bf15c397e3346264bcb7a8299c832692a96ef211968ad4d07a64dfb`

Credentials came from `chill-evidence-store-spike-credentials`. No client Pod
mounted a Kubernetes API token, and no credential value appeared in the Job
logs or repository.

## Result

The writer committed this object:

- bucket: `chill-profiling-evidence`
- key:
  `runs/sha256-165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42.json`

The verifier downloaded the object and reported:

```text
sha256: 165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42
size: 389364
result: exact-match
```

The writer and verifier Pods both exited with code 0. The writer Job eventually
reported `Complete=True` after the edge Pod completion propagated through
KubeEdge.

## Boundary established

The current testbed supports an authenticated evidence path from an edge worker
to a CHILL-managed S3-compatible store using only an internal Kubernetes Service
FQDN and a referenced Secret. A separate cloud-side reader can reconstruct and
verify the exact stored bytes without trusting the writer receipt.

This observation does not yet establish recovery after store or verifier
restart, partial-upload visibility, TLS confidentiality, credential least
privilege, retention, or garbage collection.

## Duplicate-write extension

The same retained key was exercised again from Lattepanda after the first
round trip.

### Same key and same bytes

`chill-evidence-store-put-2zl9g` attempted the same conditional create. The
store rejected the create because the key already existed; the writer then
downloaded the existing object and compared exact digest and size. It exited 0
with:

```text
outcome: existing-exact-match
```

No new measurement or overwrite was required.

### Same key and different bytes

`chill-evidence-store-conflict-jsfrd` attempted the same key with a 52-byte
candidate whose SHA-256 was
`a84d90a7b482ac1795b187894b09cf49df6af5f56862d9cdf24a6426d4b106f1`.
The conditional create returned `PreconditionFailed/412`. The conflict probe
downloaded the retained object and confirmed that it still had the original
389,364-byte size and `165d...fe42` digest.

Finally, the independent cloud-side Job
`chill-evidence-store-verify-mj9nw` downloaded the object again and reported
`exact-match` for the original bytes.

This establishes the first immutable-create rule for the spike:

- same key plus exact bytes is an idempotent existing-object success;
- same key plus different bytes is an immutable conflict; and
- resolving a conditional-create conflict requires reading and hashing the
  retained object rather than trusting the key or writer claim.
