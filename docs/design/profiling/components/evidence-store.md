# Evidence Store Contract

> Navigation: [Component contracts](README.md) · [Profiling design index](../README.md)

## Status and purpose

Implemented and live-verified for the first CPU ONNX Runtime `ProfilingRun` path.
CHILL owns immutable evidence and Kubernetes management; the S3-compatible engine
is replaceable. This is not a profile database, research catalog, scheduler
history store, or general data platform.

The testbed verified internal Service FQDN access from Lattepanda with the
EdgeMesh integration label, Secret credentials without a worker API token,
authenticated PUT and independent GET of a 389,364-byte bundle, conditional-create
overwrite prevention, and recovery through the retained PVC after Pod replacement.
This proves one complete object's immutability and process/Pod survival, not Node
or Ceph failure, HA, backup, TLS, credential rotation, retention, or garbage collection.

## Responsibility and dependency boundary

| Owner | Contract |
|---|---|
| `internal/evidencestore` | Product-neutral location, reference, receipt, immutable-create outcomes, typed errors, size enforcement; no Kubernetes, Operator, Profiler, or CHILL API imports |
| `internal/evidencestore/s3` | Conditional create, retrieval, S3 error translation, cancellation, response cleanup; sole S3 client dependency |
| CHILL Operator / `internal/operator/evidencestore` | Deployment, ClusterIP Service, Secret, persistent storage, bootstrap Job, availability; never reads or verifies Run evidence and does not import the S3 adapter |
| Measurement worker | Assemble runtime/power raw bytes, immutable-create raw measurement within its byte budget, return bounded receipt; no Kubernetes storage/Service discovery |
| Profiler controller | Validate raw evidence once at finalization; bind raw Pod UID, target Node, actuation, Job success/completion and finalize time; publish immutable execution evidence and compact reference |
| Kubernetes, CSI, EdgeMesh, engine | Native lifecycle, volumes, edge Service routing, stored-byte serving |

## Dependency direction

Worker/Profiler composition roots depend on the neutral package and its S3
adapter; the Operator starts from `ChillSystem` desired state. Package changes do
not authorize changes across those ownership boundaries.

## Immutable object contract

- Reference: logical store, opaque key, producer metadata, byte size.
- Create: conditional create, never overwrite. Candidate bytes must match their
  declared size before upload. Existing-key outcome preserves the retained winner;
  the store does not download it to compare candidate bytes.
- Read: cap actual bytes independently of the reference's declared Size. Streaming
  and caller-side `limit+1` reads enforce the bound; Size is not content proof.
- Stable errors: not-found, authentication, precondition, timeout, transport,
  content mismatch.
- One configured physical S3 bucket binds a logical store; backend bucket naming
  never replaces logical identity. One adapter, no backend plugin registry.
- Receipt adds schema version and create outcome; excludes endpoint, credentials,
  samples, Pod snapshot, backend response. It is a hint, never verification proof.
- Location recovery reads the retained winner without requiring deterministic-key
  spelling or receipt/candidate byte equality. Invalid retained objects are never
  replaced.
- Finalization owns bounded raw decoding and one-time raw runtime/observer
  validation. It binds raw Pod UID, target Node, actuation, successful measurement
  Job completion and finalize time. Raw RunConfig is execution authority; the
  finalized envelope's Config copy is provenance.
- Retained execution readers bound actual bytes and decode the current schema
  only. They do not revalidate raw semantics, execution facts, workload
  self-consistency or spec/status/reference/provenance copies.
- Finalization independently reads the immutable winner after conditional create,
  including after a race; a different candidate never overwrites the winner.
  This retained read is decode-only, not a second validation boundary.
- Execution evidence v1alpha18 stores no container terminal records or Node
  workload snapshot. Finalization does not inspect measurement/load container
  terminal states or recheck raw workload self-consistency. Raw load counts prove
  load success; Job status determines finished/failed execution.
- This is a single stored-contract cutover under
  [ADR 0019](../../../adr/0019-durable-stored-contract-compatibility.md).
  Operators delete affected old working objects; there is no compatibility
  reader, migration or rewrite of archived scientific raw records.
- Immutable decode contradictions and finalization validation failures
  terminalize as `EvidenceInvalid`. API/storage IO failures stay retryable.
- Scientific admission was removed (`788bcf93`); retained evidence supports
  execution and numeric derivation.

## Kubernetes management contract

The explicitly enabled root component is a sibling of Profiler, not its operand:
`ChillSystem.spec.evidenceStore.enabled=true` with
`spec.evidenceStore.configRef.name` (example: `chill-evidence-store-config`).
The public API exposes only enablement/configuration reference, not backend
endpoint, bucket, credentials, engine, or storage details; none are Run inputs.

In the management namespace (currently `chill-system`), the Operator owns:

- one single-replica Deployment with pinned engine image;
- one ClusterIP Service with the current EdgeMesh integration label;
- one credential Secret;
- one PVC with configured StorageClass/requested size; and
- one bounded, idempotent bootstrap Job ensuring the logical bucket through the
  internal Service after Deployment and Service become usable.

`EvidenceStoreAvailable` requires bootstrap success. Existing-bucket rerun is a
no-op. The Job writes no Run evidence, owns no retention policy, and is outside
worker/Profiler execution. Bounded Kubernetes retries leave completion/failure
observable; failures are not recreated indefinitely. A changed bootstrap contract
or explicit Job removal permits another attempt.

The bootstrap Job records the source ConfigMap `resourceVersion`. Profiler accepts
a binding only when the current availability condition and an owned completed Job
attest that same current revision, preventing use of an unbootstrapped update.
Workers receive Service delivery/Secret references through the Job, never caller
URLs, Node IPs, or NodePorts. The current Job endpoint binding is specified by the
[worker contract](measurement-worker.md#runconfig-and-delivery-inputs).

Deployment/Service are replaceable; disabling/deleting the operand never
cascade-deletes PVC or objects. No automatic garbage collection. Credentials are
created once in an immutable retained Secret; rotation is deferred. PVC growth
requires StorageClass/CSI expansion support; shrink and StorageClass replacement
are rejected. Availability does not copy native replica, PVC, or engine status.

The engine's internal IAM is enabled in `/data/.iam` on the retained PVC; a
non-root init container creates that directory before the engine starts.
Evaluation writers use a separate `user` account and a bucket policy granting
only ListBucket/GetObject/PutObject, without DeleteObject. Root credentials remain
the management/bootstrap account. IAM metadata must not be exposed as an S3 bucket
or object; deployment checks verify both root and evaluation-user access boundaries.

## Implemented scope and remaining boundaries

Internal contract/S3 adapter, Operator enable/update/disable with PVC retention,
and Profiler receipt verification/execution finalization are implemented; operand
lifecycle and retained-object Pod replacement were live-verified. Conditional
create/existing-key behavior must remain deterministic, S3 failures distinguishable,
and evidence/credentials absent from status/logs except compact permitted references.

Out of scope/unproven: metadata/search catalogs; profile, scheduler, campaign and
research-export storage; TLS automation/rotation; retention/GC/backup/disaster
recovery; multi-replica storage/storage-node failover; changes to another system's
MinIO, WebDAV, NFS, or monitoring lifecycle.
