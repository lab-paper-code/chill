# CHILL Design Documents

Each collection owns its implementation boundary. Requirements belong in
[`docs/spec/`](../spec/); durable decisions belong in [`docs/adr/`](../adr/).
Current source establishes implementation facts.

## Entry points

| Design area | Start here | Scope |
|---|---|---|
| Runtime, artifact, and profiling | [Profiling](profiling/README.md) | Immutable execution, raw evidence, and numeric derivation |
| Serving | [Serving](serving/README.md) | Workload, runtime, planner, measurement, and operations |
| Evaluation | [Evaluation scope](evaluation/scope.md) | External-evaluator inputs, judgment, publication, and decision rights |
| Experiment execution | [EvaluationRun execution](profiling/experiment-execution.md) | Profiler controllers, harness Jobs, and immutable results |
| Query API | [Query API](query-api/README.md) | Query ownership and Dashboard/CLI responsibilities |
| Dashboard | [Dashboard implementation](dashboard/README.md) | Web, BFF, live transport, and display boundaries |
| Code intelligence | [Code intelligence](code-intelligence/README.md) | Source navigation, package direction, and deployment evidence |
| Node platform | [Platform](platform/README.md) | Discovery, power actuation, and resource observations |

## Placement

Keep domain entry points short. Reusable profiling contracts live in
`profiling/components/`; runtime compatibility and provenance boundaries live in
`profiling/foundations/`. Link requirements. Do not duplicate them.
