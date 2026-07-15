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

This observation does not yet establish controller reconstruction after its own
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

## Store Pod replacement extension

The running evidence-store Pod was deleted after the immutable round-trip tests:

- old Pod: `chill-evidence-store-spike-5bfc9c749-cpdlb`
- old Pod UID: `c0769967-85bf-467f-8928-5b54abb90b9c`

The Deployment created a different Pod and returned it to Ready:

- new Pod: `chill-evidence-store-spike-5bfc9c749-sk9r7`
- new Pod UID: `9911489c-8840-4e5e-a8c9-0e5ad91ed5d8`
- restart count: `0`
- Node: `genesis2`

The PVC identity and backing PV remained unchanged across replacement:

- PVC UID: `4d3db5a9-1dd5-4084-8251-03884c2980a0`
- PV: `pvc-4d3db5a9-1dd5-4084-8251-03884c2980a0`
- phase: `Bound`

After the new store process became Ready, the fresh verifier Job
`chill-evidence-store-verify-5zhjq` retrieved the previously written object and
again reported the original 389,364-byte size, `165d...fe42` digest, and
`exact-match`.

This establishes persistence across evidence-store process and Pod replacement
through the retained PVC. It does not establish cloud Node failure, Ceph
failure, multi-replica availability, backup, or disaster recovery.

## Internal client promotion regression

The retained regression was repeated with the promoted
`internal/evidencestore` contract and S3 adapter through the pinned CHILL spike
client image:

`155.230.35.213:5000/chill/evidence-store-client@sha256:74d527e3dd94297b42e8629f79a3d2d265b601e678ff94118857d8a8154ac5fe`

The new path used a separate deterministic key so that the adapter's initial
create was observed without changing the earlier retained object:

`runs/internal-plan1/sha256-165d50e316de2b3d7da0728df1245aaf56f8f5a407668db1c24a756b6ba3fe42.json`

The observed sequence was:

1. `chill-evidence-store-put-x4zzn` ran on Lattepanda and returned `Created`
   for the 389,364-byte object.
2. `chill-evidence-store-put-gjkbf` repeated the same immutable write on
   Lattepanda and returned `ExistingExactMatch`.
3. `chill-evidence-store-verify-x9dfm` ran independently on `genesis2` and
   returned `ExactMatch` for the expected size and SHA-256.
4. `chill-evidence-store-conflict-pj6cb` ran on Lattepanda with a different
   51-byte candidate, observed the immutable conflict, and returned
   `RejectedDifferentBytes` only after verifying that the retained object was
   unchanged.

The store Pod was then replaced:

- old Pod: `chill-evidence-store-spike-5bfc9c749-sk9r7`
- old Pod UID: `9911489c-8840-4e5e-a8c9-0e5ad91ed5d8`
- new Pod: `chill-evidence-store-spike-5bfc9c749-vt92j`
- new Pod UID: `27c5669d-f28b-442b-a600-3e25877a8a37`

The PVC UID remained
`4d3db5a9-1dd5-4084-8251-03884c2980a0`, the backing PV remained
`pvc-4d3db5a9-1dd5-4084-8251-03884c2980a0`, and the PVC stayed `Bound`.
After the new store Pod became Ready, the fresh verifier Job
`chill-evidence-store-verify-2dqc4` again returned `ExactMatch` from
`genesis2`.

This regression demonstrates that the production-boundary Go implementation,
not a parallel shell implementation, preserves the spike's create,
idempotency, conflict, independent verification, and Pod-replacement behavior.
It does not broaden the earlier durability or availability claim limits.

## Operator-managed operand lifecycle

Plan 2 was exercised through the live `ChillSystem` and the promoted Operator
using operator image digest
`sha256:c25d40e1bc27396dfcd0a61bd87f50d142d062ac2a08339e0635bc06022314c5`.

After `spec.evidenceStore.enabled` became true, the observed order was:

1. the immutable credential Secret and retained PVC were created;
2. the single-replica Deployment and ClusterIP Service became available;
3. the bootstrap Job was created only after Deployment availability and
   returned bucket outcome `Created`; and
4. `EvidenceStoreAvailable` changed from `EvidenceStoreProgressing` to
   `EvidenceStoreReady`.

`chill-evidence-store-put-h8gp6` ran on Lattepanda through
`chill-evidence-store.chill-system.svc.cluster.local`, used the managed Secret,
and returned `Created` for the 389,364-byte object under
`runs/managed-plan2/`. The independent Job
`chill-evidence-store-verify-q6qw4` returned `ExactMatch` for the expected size
and SHA-256.

Updating the configured storage request from `1Gi` to `2Gi` preserved all
identities:

- Secret UID: `df513ccd-3c3b-4566-b539-366c080d3909`;
- PVC UID: `e3d6c066-8f6b-4d83-a8a2-116c37ace771`; and
- PV: `pvc-e3d6c066-8f6b-4d83-a8a2-116c37ace771`.

The PVC reached a `2Gi` requested and reported capacity. Disabling the operand
then removed the owned Deployment, Service, and bootstrap Job, removed the
`EvidenceStoreAvailable` Condition, and retained the same immutable Secret,
PVC, PV, ConfigMap, and stored evidence. The root `ChillSystem` remained Ready.

One earlier PUT Job is excluded from the storage result: a broad test-manifest
string substitution changed the fixture ConfigMap name, so the client exited
before contacting the store because `/fixture/evidence.json` was absent. The
corrected Jobs above changed only endpoint, Secret, and object key.
