# CHILL Design Documents

Use this directory as a set of domain-specific design collections, not as one
flat list of equally authoritative files.

## Entry points

| Design area | Start here | Scope |
|---|---|---|
| Runtime, artifact, and profiling | [profiling/README.md](profiling/README.md) | Ordered profiling plans, reusable component contracts, and supporting fact audits |
| Serving | [serving/README.md](serving/README.md) | The consumer contract for inference, the entry point that distributes requests, and the routing decision it applies |
| Operator console | [operator-console/README.md](operator-console/README.md) | Operator-facing concepts, workflows, interfaces, use cases, and requirements |
| Dashboard implementation | [dashboard/README.md](dashboard/README.md) | Pinned web template, core architecture, API ownership and gaps, and full Dashboard SRS acceptance |
| Code intelligence and architecture analysis | [code-intelligence/README.md](code-intelligence/README.md) | Current semantic-navigation, package-boundary, optional indexing, and Helm-topology evidence contract |
| Architecture rethink (proposal, not accepted) | [architecture-rethink/README.md](architecture-rethink/README.md) | Alternatives B1–B3 to the current global replay scheduler and multi-writer node state, migration path M0–M5, and proposed spec sentence changes awaiting approval |

Architectural decisions that must remain stable across design areas belong in
[`docs/adr/`](../adr/). Product and system requirements belong in
[`docs/spec/`](../spec/).

## Placement rule

When adding a profiling document:

- place an ordered implementation boundary in `profiling/plans/`;
- place a reusable subsystem design in `profiling/components/`;
- place fact audits and boundary-derivation records in
  `profiling/foundations/`; and
- update `profiling/README.md` so an agent can discover the document from the
  domain entry point.

Create another domain directory with its own `README.md` when a design does not
belong to profiling or the operator console. Avoid adding new standalone files
to this directory.
