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
| Measurement worker | Assemble runtime/power raw bytes, calculate identity, immutable-create raw measurement, return bounded receipt; no Kubernetes storage/Service discovery |
| Profiler controller | Independently retrieve receipt target, validate schema/links, combine terminal Kubernetes facts into separate immutable execution evidence, publish compact verified reference; never reconcile storage operand |
| Kubernetes, CSI, EdgeMesh, engine | Native lifecycle, volumes, edge Service routing, stored-byte serving |

## Dependency direction

Worker/Profiler composition roots depend on the neutral package and its S3
adapter; the Operator starts from `ChillSystem` desired state. Package changes do
not authorize changes across those ownership boundaries.

## Immutable object contract

- Reference: logical store, opaque key, producer metadata, byte size.
- Create: new object or existing key, never overwrite. Candidate bytes must match
  declared size before conditional create; mismatch returns content-mismatch and
  leaves no new object.
- Read: independent and byte-bounded, using streaming or a bounded temporary file,
  not CR status or an unbounded in-memory byte slice.
- Stable errors: not-found, authentication, precondition, timeout, transport,
  content mismatch.
- One configured physical S3 bucket binds a logical store; backend bucket naming
  never replaces logical identity. One adapter, no backend plugin registry.
- Receipt adds schema version and create outcome; excludes endpoint, credentials,
  samples, Pod snapshot, backend response. It is a hint, never verification proof.
- Location-only recovery handles deterministic keys committed before reference
  persistence: bound bytes, strict-decode one object, validate schema/Run links.
  Invalid existing objects are never replaced.
- The evidence loader is the single read-side validation boundary: size-bound,
  strict schema decode and cross-record links produce opaque active/idle evidence.
  Finalization retains independent read-after-write decode. Scientific admission
  was removed (`788bcf93`); retained evidence supports execution and numeric derivation.

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
