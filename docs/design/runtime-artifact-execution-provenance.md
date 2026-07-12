# Runtime and Artifact Execution Provenance

## Status

Step 3 complete. This document defines provenance requirements and guardrails
for high-impact execution claims. It does not define final enums, Go structs,
CRD fields, compatibility rules, or an execution-contract package.

## Inputs

This step builds on:

- [`runtime-artifact-execution-fact-audit.md`](runtime-artifact-execution-fact-audit.md)
- [`runtime-artifact-execution-commonality.md`](runtime-artifact-execution-commonality.md)

The four Step 1 audit labels—verified declaration, run observation, derived
decision, spike constant, and unknown—remain useful for reasoning. They are not
sufficient as a production provenance enum.

## Principal finding

Provenance is not a trust-level label. It must constrain what a claim is
allowed to mean:

```text
who or what supplied the claim
        + the identity of that source or snapshot
        + what verification was actually performed
        + the scope in which the claim is valid
        + time/version context when the source is mutable
```

`Declared`, `Observed`, `Derived`, or `Trusted` alone cannot provide these
properties.

## Lifecycle is structural, not provenance

The Step 2 boundaries remain separate:

```text
catalog/artifact declaration
runtime-specific desired decision
Kubernetes workload intent
post-start observation/evidence
```

A provenance record does not permit these lifecycles to be merged. For
example, a requested provider and an observed registered provider are both
valid claims in different sections. Provenance must expose their mismatch, not
select one using a global priority rule.

## Minimum provenance dimensions

These are requirements, not final API field names.

### Source kind

Identifies the authority or producer class, such as:

- immutable artifact metadata
- immutable runtime image/package declaration
- mutable Kubernetes object snapshot
- host component probe
- runtime process/session observation
- Kubernetes allocation observation
- CHILL policy derivation

A kind does not establish trust by itself.

### Source identity

Identifies the exact source supporting the claim:

- artifact content digest
- pulled image digest, not only an image tag
- Node UID plus resourceVersion for a retained Node snapshot
- Pod UID, container identity, and Run identity
- runtime component identity where it can be observed

Names, tags, endpoints, and file paths are locators. They are not immutable
evidence identities.

### Verification depth

Records what was actually checked. Examples include:

- declared only
- packaged in an identified image
- content digest matched
- API field observed
- Kubernetes allocation confirmed for a Pod
- runtime availability reported
- runtime initialized or model loaded
- selected batch executed
- output shape or semantics validated
- graph assignment observed

These checks are not interchangeable. In particular, provider availability,
session registration, selected provider, valid output, and graph placement are
different verification depths.

### Claim scope

Limits where a claim may be reused:

- artifact-wide
- runtime-image-wide
- Node at one snapshot
- Pod or Run
- process or session
- selected artifact and batch
- specific compiled engine/profile

A selected-batch success must not become artifact-wide batch capability. A
session provider list must not become future Node capability.

### Time and version semantics

Mutable sources require snapshot or version context and an observation time.
An observation time does not replace a resourceVersion or content identity.

Timestamp semantics must be explicit:

- producer/device timestamp
- observer receipt timestamp
- no source timestamp available

PowerObserver receipt time must never be represented as a Shelly device
measurement time.

Immutable content normally needs a digest rather than repeated timestamps on
every claim.

### Derivation identity

A derived decision must identify:

- policy or rule identity and version
- all material input evidence/snapshot identities
- the output group produced atomically by that rule

`derivedAt` alone does not make a decision reproducible or current. When an
input snapshot changes, the old derivation remains historical evidence and is
not silently reused as a current scheduling decision.

### Unknown state

`Unknown` is an epistemic/result state, not a provenance kind. An actionable
unknown must identify the required claim and why it cannot currently be
established. The later stable vocabulary may need to distinguish:

- required source absent
- source present but self-declared or untrusted for this claim
- source snapshot stale or identity mismatch
- comparison unsupported
- evidence ambiguous or conflicting
- observation or verification failed

This step does not finalize those reason codes. Unknown and unrecognized forms
must fail closed when the claim is required.

## Claim-group granularity

Do not wrap every scalar as `{value, source, observedAt, verification}`.

Values sharing the same source snapshot, rule, scope, and lifecycle form one
atomic claim/evidence group with one provenance header and plain typed values.
If children have different authorities or scopes, split the group instead of
attaching one broad provenance label.

Preferred conceptual shape:

```text
Node snapshot evidence
  provenance: Node UID/resourceVersion, observedAt
  values: allocatable CPU and fields actually consumed

CPU execution decision
  provenance: policy identity/version + Node snapshot reference
  outputs: CPU limit and ORT thread settings

Runtime enforcement evidence
  provenance: Run/Pod/container/image identity, observation window
  values: cgroup quota, affinity, effective runtime settings
```

Rejected leaf-wrapper shape:

```text
cpuLimit.value + full provenance
ortIntra.value + duplicated full provenance
ortInter.value + duplicated full provenance
```

Leaf wrappers duplicate metadata, allow siblings to reference inconsistent
snapshots, complicate CRD validation and equality, and create provenance
theater without improving verification.

## Current-path application

### CPU

```text
Node snapshot
  source: Kubernetes Node UID/resourceVersion
  observed: status.allocatable.cpu
        |
        v
CHILL decision
  rule: CPU budget/thread policy identity and version
  inputs: Node snapshot reference
  outputs: limit, ORT intra-op, ORT inter-op
        |
        v
Run evidence
  scope: Pod/container/Run
  observed: cpu.max, affinity/cpuset, runtime settings, throttling
```

The current Node name and `derivedAt` are insufficient snapshot identity.
`OnePodPerNodeFullCPU` remains unverified unless separate enforcement evidence
exists.

### RKNN

- `SUPPORTED_BATCHES`: workload declaration, not artifact-verified capability
- selected batch with valid output: Run-scoped execution observation
- toolkit-lite version: image/package declaration when bound to image digest
- host `librknnrt` and driver identity: currently Unknown
- `/dev/dri` access and successful initialization: Run-scoped access
  observation, not Kubernetes allocation evidence
- NPU isolation: Unknown

### Jetson

- provider request and FP16/INT8 flags: desired runtime configuration
- ORT version and registered providers: process/session observation
- TensorRT graph placement and effective kernel precision: Unknown
- `nvidia.com/gpu: 1` request: Kubernetes workload intent
- successful GPU allocation: Pod/Run-scoped Kubernetes observation
- hardcoded L4T and power mode: not current Run observations
- Pod-local cache existence: execution filesystem observation; engine/profile
  identity remains Unknown

## Staleness and authority

Kubernetes remains authoritative for current Ready, taints, allocatable, and
resource capacity. CHILL may retain the snapshot used by an immutable Run, but
that copy is historical Run evidence, not a second current-status authority.

Similarly:

- a mutable image tag does not preserve runtime identity when its digest moves
- a runtime's self-reported version is process evidence, not supply-chain
  attestation
- a verified model digest does not verify adjacent artifact metadata or the
  runtime image
- hostPath presence does not identify the mounted library or prove ABI
  compatibility

## Acceptance examples

Any later provenance representation must satisfy these checks:

1. Manifest `powerMode=15W` without a live read cannot become observed current
   power mode.
2. TensorRT being first in the registered provider list cannot claim full
   TensorRT graph execution.
3. A changed Node resourceVersion invalidates reuse of a CPU derivation for a
   new current decision; the old result remains historical evidence.
4. GPU request and successful GPU allocation remain separate claims.
5. The same runtime image tag resolving to a different digest is a different
   runtime input identity.
6. A Shelly receipt timestamp cannot be synthesized into a device timestamp.
7. Self-declared RKNN ABI metadata cannot produce a verified compatibility
   claim.
8. Unknown provenance/verification forms fail closed when required.

## Adversarial review disposition

Accepted:

- a single provenance enum mixes source, trust, method, scope, and time
- self-declaration and runtime self-report must not be laundered into reusable
  capability
- derived decisions require rule and input identities
- mutable Kubernetes state must remain authoritative in Kubernetes
- unknown needs an actionable reason rather than a bare null
- provenance belongs at uniform claim-group boundaries, not every scalar

Narrowed:

- source kind enums can still be useful routing vocabulary, provided they do
  not imply equal trust or replace source identity and verification depth
- timestamps are required for mutable observations, but immutable digests do
  not need redundant per-field times
- retained Kubernetes snapshots are acceptable as immutable Run evidence, but
  not as current cluster status

Rejected:

- the idea that richer provenance metadata can make a mixed-lifecycle object
  safe. The object must first be split according to Step 2.

## Step 3 conclusion

Production provenance should be selective, claim-scoped evidence metadata—not
a universal trust label and not a wrapper around every value.

The later design must preserve this chain:

```text
identified input or snapshot
        -> identified CHILL rule/decision
        -> native Kubernetes workload intent
        -> identified Run observation and validation
```

Step 4 may now separate static compatibility evaluation from execution
decision and Run validation. Provenance must support that boundary rather than
becoming the boundary itself.
