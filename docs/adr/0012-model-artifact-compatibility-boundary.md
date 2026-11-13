# Model Artifact Compatibility Boundary

## Status

Accepted

## Context

Configuration enumeration requires a declared executable artifact and runtime path. Logical model identity alone cannot establish execution compatibility.

## Decision

- A model names the logical workload and its semantics.
- An artifact variant carries one or more model files from one immutable OCI supply image and declares execution requirements.
- A runtime path identifies runtime family and backend or execution provider. An explicit workload/runtime declaration supplies runtime availability.
- A profile records a measured execution state, including its preprocessing and dataset identity under [Raw-Sample Preprocessing Boundary](0021-raw-sample-preprocessing-boundary.md).

Keep DeviceClass limited to stable hardware capabilities. Do not store installed runtime versions, providers or serving images there.

The artifact contract represents the requirements consumed by supported execution paths: format, architecture, accelerator, runtime/backend, target platform and batch capability. RKNN artifacts declare typed input tensors; preprocessing supplies inputs for other formats.

Do not infer runtime memory requirements from artifact file size. Admit memory or software-version constraints only when their meaning, source and comparison are implemented.

Use the shared pure evaluator in `internal/modelcompat`:

- `Compatible`: declared requirements match declared capabilities and runtime path.
- `Incompatible`: a declared requirement explicitly conflicts.
- `Unknown`: a required fact is absent, unsupported or lacks an authoritative source.

Both `Unknown` and `Incompatible` exclude a state from configuration enumeration. Compatibility establishes static execution support. Profile admission, SLO feasibility and current Kubernetes eligibility require separate evidence.

Do not persist derived `compatibleClasses` or inverse class/model indexes. Evaluate the catalog cross-product at decision time.

## Consequences

Profilers, enumerators and serving consumers share one evaluator and reason vocabulary. A failed measurement remains evidence about its selected artifact/path; it does not redefine the logical model's compatibility.

