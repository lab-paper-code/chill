# CHILL Design Documents

Use this directory as a set of domain-specific design collections, not as one
flat list of equally authoritative files.

## Entry points

| Design area | Start here | Scope |
|---|---|---|
| Runtime, artifact, and profiling | [profiling/README.md](profiling/README.md) | Active component contracts and retained measurement facts; completed plans are summaries |
| Serving | [serving/README.md](serving/README.md) | Active Workload/runtime/κ planner/measurement/operations contracts and qualified evidence |
| Serving evidence API | [serving/observability-split.md §17](serving/observability-split.md#17-e5-증거-api-2026-10-30) | Raw export, resources/cluster streams, read-only meters and deployment/acceptance boundary (ADR 0030) |
| Operator console | [operator-console/README.md](operator-console/README.md) | Active operator model and distinct Dashboard/CLI responsibilities; requirements stay in SRS |
| Dashboard implementation | [dashboard/README.md](dashboard/README.md) | Active web/API/live ownership and acceptance boundary |
| Code intelligence and architecture analysis | [code-intelligence/README.md](code-intelligence/README.md) | Semantic navigation, package boundaries, optional indexing and Helm topology evidence |
| Node platform | [platform/README.md](platform/README.md) | Active discovery/actuation agents, Node power generation, resource collection and M5 outcome |
| Architecture rethink (proposal, not accepted) | [architecture-rethink/README.md](architecture-rethink/README.md) | One approval-pending summary: B1–B3, M0–M5 and unresolved decisions |

Architectural decisions that must remain stable across design areas belong in
[`docs/adr/`](../adr/). Product and system requirements belong in
[`docs/spec/`](../spec/).

## Placement rule

Profiling implementation boundaries belong in `profiling/plans/`, reusable contracts in `profiling/components/`, fact audits in `profiling/foundations/`. Keep each domain README as a short file/status index, link to authoritative requirements rather than duplicating them, and do not add standalone designs at this directory root.
