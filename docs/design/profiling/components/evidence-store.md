# Evidence Store Internal Promotion

> Navigation: [Profiling design index](../README.md)

## Status

Approved high-level design. Plans 1 and 2 are implemented and live-verified.
Plan 3 remains review-gated.

## Purpose

Promote the immutable evidence-storage contract proven by the isolated spike
into CHILL. CHILL owns the data and Kubernetes management boundary while the
S3-compatible storage engine remains a replaceable dependency.

The first promoted path answers one question:

> Can CHILL producers commit immutable evidence objects and can a separate
> reader later reconstruct and verify the exact retained bytes?

This is not a general data platform, profile database, research catalog, or
scheduler history store.

## Promotion evidence and claim limit

The current testbed demonstrated:

- Lattepanda reaches the CHILL-managed store through its internal Kubernetes
  Service FQDN and the current EdgeMesh integration label;
- credentials come from a Secret and the edge worker has no Kubernetes API
  token;
- the existing 389,364-byte evidence bundle survives authenticated PUT and
  independent GET with exact SHA-256 and size;
- same key plus exact bytes is idempotent, while different bytes are rejected
  without overwrite; and
- a new store Pod recovers the same object through the retained PVC.

The resulting claim is deliberately narrow: one complete object is immutable
and survives process and Pod replacement. Node or Ceph failure, HA, backup,
TLS, credential rotation, retention, and garbage collection remain unproven.

## Before and after

```text
Before

spike Jobs -> AWS CLI -> spike store
human-run scripts interpret receipts and verification output
```

```text
After the complete promotion

CHILL Operator -> manages Evidence Store operand

measurement worker
  -> internal evidence writer
  -> S3 adapter
  -> immutable raw-measurement object + bounded receipt

Profiler controller
  -> internal evidence reader
  -> independent raw-object verification
  -> internal evidence writer
  -> immutable execution-evidence record + compact verified reference
```

The three parts are promoted separately. An internal storage module does not
authorize an Operator or Profiler controller change.

## Responsibility boundary

| Owner | Responsibility |
|---|---|
| `internal/evidencestore` | Product-neutral location, reference, receipt, immutable-create outcomes, typed errors, exact digest and size verification |
| S3 adapter | Conditional create, object retrieval, S3 error translation, cancellation and response cleanup |
| CHILL Operator | Deployment, ClusterIP Service, Secret, persistent-storage attachment, and component availability |
| Measurement worker | Assemble raw runtime and power bytes, calculate identity, invoke immutable create, return a bounded receipt |
| Profiler controller | Treat receipt as a hint, retrieve independently, verify raw content, combine terminal Kubernetes facts into a separate immutable execution-evidence record, publish compact status |
| Kubernetes, CSI, EdgeMesh, storage engine | Native workload lifecycle, volume mechanics, edge Service routing, and stored-byte serving |

Important exclusions follow from this table:

- the Operator never reads or verifies Run evidence;
- the worker never discovers Services or storage through the Kubernetes API;
- the Profiler never reconciles the storage operand; and
- the product-neutral package imports no Kubernetes, Operator, Profiler, or
  CHILL API types.

## Dependency direction

```text
worker / Profiler composition root
          |                         |
          v                         v
 internal/evidencestore <--- internal/evidencestore/s3
                                    |
                                    v
                         S3-compatible endpoint
```

```text
ChillSystem desired state
          |
          v
internal/operator/evidencestore
          |
          v
Deployment + Service + Secret + persistent storage + bootstrap Job
```

The Operator does not import the S3 adapter. The S3 client dependency is
confined to the adapter.

## First internal contract

The first package needs only four concepts:

- expected object identity: logical store, opaque key, SHA-256, and byte size;
- immutable create: new object, exact existing object, or conflict;
- independent read and verification; and
- stable error categories for not-found, authentication, precondition,
  timeout, transport, and content mismatch.

The S3 adapter binds that logical store to one configured physical bucket.
Backend bucket naming is delivery configuration and never replaces the logical
store carried by an object identity or receipt.

The receipt adds schema version and create outcome. It contains no endpoint,
credential, raw samples, Pod snapshot, or backend response. A receipt alone is
never sufficient for admission.

A location-only read exists solely to recover an immutable object when its
deterministic key was committed before the controller persisted the resulting
digest and size. Location is not identity: these bytes remain untrusted until
the caller derives their content identity, validates the schema and Run links,
and confirms canonical encoding. An invalid existing object is never replaced.

The implementation must support bounded streaming or a bounded temporary file;
it must not force future evidence into CR status or an unbounded in-memory byte
slice. The first adapter supports one S3-compatible path without a plugin
registry or multiple backend abstractions.

An immutable create succeeds only when the candidate bytes match the expected
size and digest. The first adapter verifies the candidate before conditional
create; a mismatch returns a content-mismatch error and leaves no new object.

## Kubernetes management contract

The operand lives in the existing CHILL management namespace, currently
`chill-system`. Its first resource set is:

- one single-replica Deployment with a pinned engine image;
- one ClusterIP Service with the current EdgeMesh integration label;
- one credential Secret;
- one PVC with configured StorageClass and requested size; and
- one bounded, idempotent bootstrap Job that ensures the logical evidence
  bucket exists through the internal Service.

The storage engine starts independently of the logical bucket. The Operator
therefore creates the bootstrap Job after the Service and Deployment are
usable, and reports `EvidenceStoreAvailable` only after that Job succeeds. A
successful rerun against an existing bucket is a no-op. The Job provisions no
Run evidence, owns no retention policy, and is not part of the worker or
Profiler execution path.

The Job uses bounded Kubernetes retry and remains observable after completion
or failure. A failed Job is not recreated indefinitely; a changed bootstrap
contract or explicit Job removal permits a new attempt.

Workers receive the internal Service FQDN and referenced Secret through their
Job. Node IPs, NodePorts, and caller-selected URLs are not Run inputs.

Deployment and Service are replaceable. Evidence bytes are not. Disabling or
deleting the operand must not cascade-delete the PVC or objects, and the first
implementation performs no automatic garbage collection. Credentials are
created once and retained in an immutable Secret; automatic rotation is
deferred.

The retained PVC may request a capacity increase when the StorageClass and CSI
driver support expansion. Capacity reduction and StorageClass replacement are
rejected rather than interpreted as safe updates.

Evidence Store is an explicitly enabled root component of `ChillSystem`:

```yaml
spec:
  evidenceStore:
    enabled: true
    configRef:
      name: chill-evidence-store-config
```

The public surface contains only enablement and a reference to CHILL-managed
configuration. Backend endpoint, bucket, credentials, engine choice, and
storage details do not become Run inputs or public `ChillSystem` fields. The
operand is a sibling of the Profiler under `ChillSystem`, not a
Profiler-owned Deployment.

## Bounded promotion plan

### Plan 1: internal contract and S3 adapter — implemented

1. Add product-neutral types, ports, verification, and unit tests under
   `internal/evidencestore`.
2. Add one S3-compatible adapter in its subpackage, including pre-create
   candidate verification.
3. Replace the spike AWS CLI mechanics with a small spike executable that
   consumes the internal package.
4. Re-run only the retained regression set: create, exact duplicate, conflicting
   duplicate, independent verification, and store Pod replacement.

Stop for review. Plan 1 adds no API fields, controller watches, or managed
Kubernetes resources.

### Plan 2: Operator-managed operand

1. Add the minimal `ChillSystem` desired-state surface. — implemented
2. Add a short, reconstructable `internal/operator/evidencestore` reconciler. — implemented
3. Reconcile the Deployment, Service, Secret, persistent-storage attachment,
   and idempotent bucket bootstrap with explicit PVC retention. — implemented
4. Add one `EvidenceStoreAvailable` condition without copying native replica,
   PVC, or engine status into `ChillSystem.status`. — implemented
5. Verify operand enable/update/disable and PVC retention. — live-verified

Stop for review. The Operator still does not write or verify evidence.

### Plan 3: Profiler consumption

Begin only after the minimum `ProfilingRun` API and controller contract is
approved. Inject resolved store configuration into the worker, consume its
bounded receipt, independently verify the raw-measurement object, write and
verify a separate execution-evidence record from terminal Kubernetes facts,
and publish the compact finalized reference and Conditions.

## Acceptance boundary

Promotion is acceptable when the spike consumes the internal package rather
than retaining a second implementation, exact duplicate and conflict behavior
have deterministic tests, S3 failures remain distinguishable, raw evidence and
credentials stay out of CR status and logs, and each plan stops for review.

The following remain outside this promotion:

- metadata databases and searchable catalogs;
- profile, scheduler, campaign, and research-export storage;
- TLS automation and credential rotation;
- retention, garbage collection, backup, and disaster recovery;
- multi-replica storage and storage-node failover; and
- changes to another system's MinIO, WebDAV, NFS, or monitoring lifecycle.

## Review question

Does this design promote only the verified immutable evidence primitive while
keeping storage operation with the CHILL Operator, measurement execution with
the worker, evidence finalization with the Profiler controller, and raw bytes
outside Kubernetes status?
