# Evidence Store Contract

[Component contracts](README.md) and [Profiling design index](../README.md)

## Ownership

| Owner | Contract |
|---|---|
| `internal/evidencestore` | Neutral location, reference, receipt, typed errors and byte bounds |
| `internal/evidencestore/s3` | Conditional create, retrieval, S3 errors, cancellation and response cleanup |
| Operator | Deployment, Service, Secret, retained PVC, bootstrap Job and availability |
| Measurement worker | Assemble raw bytes, immutable-create within budget and return bounded receipt |
| Profiler | Validate raw once, bind execution facts and publish finalized evidence |
| Kubernetes, CSI and engine | Lifecycle, volumes, routing and stored-byte serving |

The core has no Kubernetes, Operator, Profiler or CHILL API imports. The S3
adapter depends on the core; production Operator code does not consume it.
Composition roots assemble clients.

## Immutable objects

- References contain logical store, opaque key, producer metadata and size.
- Conditional create never overwrites. Candidate bytes match declared size.
  An existing key preserves its winner without candidate-byte comparison.
- Reads cap actual bytes independently of declared size. Streaming and caller
  `limit+1` reads enforce the cap.
- Errors distinguish not-found, authentication, precondition, timeout,
  transport and content mismatch.
- A configured physical bucket binds one logical store. Receipt schema/create
  outcome is a delivery hint; endpoint, credentials, samples and snapshots stay
  outside it.
- Recovery reads the retained winner. Key spelling and duplicated
  receipt/provenance fields do not establish another execution contract.

## Finalization and retained reads

Finalization bounds and decodes raw, validates runtime/observer facts once,
and binds raw Pod UID, target Node, actuation, successful Job completion and
finalize time. Raw RunConfig is authority; the envelope's Config copy is
provenance. Finalization reads the winner after conditional create, including
races, without a second raw validation pass.

Execution evidence v1alpha18 stores no container terminal records or Node
workload snapshot. Raw load counts prove load success; Job status determines
finished/failed execution. Finalization does not inspect container terminal
states or recheck raw workload self-consistency.

Retained execution readers bound bytes and decode current wire types. They do
not repeat raw validation, workload replay or comparisons with mutable
specs/status and duplicated provenance. Immutable contradictions become terminal
`EvidenceInvalid`; API/store IO failures remain retryable.

Contract replacement follows
[Durable Stored-Contract Compatibility](../../../adr/0019-durable-stored-contract-compatibility.md).
No migration, compatibility reader or archived-raw rewrite is provided.

## Kubernetes management

`ChillSystem.spec.evidenceStore.enabled=true` enables a root component alongside
Profiler. `spec.evidenceStore.configRef.name` selects installation configuration.
Endpoint, bucket, credentials, engine and storage details are outside Run inputs.

The Operator owns a single-replica Deployment, ClusterIP Service, credential
Secret, retained PVC and bounded bootstrap Job. Bootstrap ensures the bucket
through the Service and writes no Run evidence. Availability requires bootstrap
success for the current ConfigMap resourceVersion, attested by an owned Job.

Workers receive delivery and Secret references through their Job. They do not
discover Services or read Kubernetes Secrets. In-flight Runs recover their
original binding. See [worker delivery](measurement-worker.md#runconfig-and-delivery-inputs).

Disabling/deleting the operand retains PVC and objects. There is no automatic
GC, retention, backup or disaster-recovery policy. Credentials are created once
in an immutable retained Secret. PVC growth requires CSI support; shrink and
StorageClass replacement are rejected.

The engine IAM directory is `/data/.iam` on the retained PVC, initialized by a
non-root init container. Evaluation writes use separately provisioned credentials;
permitted object operations remain an installation security boundary. Root
credentials remain management/bootstrap credentials.

## Limits

Idle baseline Run and evaluation objects use the neutral immutable contract.
The store owns no domain catalog, profile selection, scientific judgment or
research indexing. Multi-replica storage, storage-node failover, TLS automation,
credential rotation and disaster recovery require separate qualification.
