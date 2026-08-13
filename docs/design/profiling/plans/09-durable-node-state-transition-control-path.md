# Plan 9: Durable Node State-Transition Control Path

> Navigation: [Profiling design index](../README.md)
>
> Decisions: [ADR 0015](../../../adr/0015-node-power-actuation-boundary.md),
> [ADR 0017](../../../adr/0017-in-place-power-mode-reachability-boundary.md),
> [ADR 0018](../../../adr/0018-state-transition-operation-and-witness-boundary.md)

## Status

Design only. No scope in this document is approved, scheduled, or implemented.
The Run-owned actuation path remains the repository behavior. This plan does not
authorize an API, CRD, controller, RBAC, deployment, evidence-schema, or
migration change.

## Question

> Can a Node state transition be established, witnessed, and then consumed by
> many `ProfilingRun` objects, without any single Run owning, holding, or
> restoring it?

## Current boundary and design target

[`reconcileActuatedRun`](../../../../internal/profiler/controller/actuated_run.go)
ensures one Run-owned `NodeActuation`, publishes its reference, waits for a
verified and settled target before creating the Job, holds that target while
the Job is active, requests restoration after terminal execution, and withholds
the ordinary evidence lifecycle until restoration is independently verified.

The NodeActuator
[`lifecycle`](../../../../internal/nodeactuator/lifecycle.go) acquires the
Node-UID Lease, checks workload safety, observes the previous state, applies
only when necessary, independently reads the target back, settles, holds,
restores when requested, independently reads the restored state back, and
releases the Lease only after the lifecycle reaches a safe terminal boundary.
Command completion is not state convergence.

This plan assigns the transition and the tenure of its established state to a
durable operation outside every Run. A Run declares and binds to a state
precondition; it does not create, own, advance, retain, retire, or delete the
operation. Transition execution, transition confirmation, and Run evidence
admission remain separate responsibilities.

Boot identity in actuation observations and execution evidence at `6619649`,
and the terminal no-requeue outcome for permanently invalid Run evidence at
`8b00f1c`, are input contracts. This plan consumes those contracts without
defining another boot-identity format or another permanent-evidence retry rule.

## Checkpoint 1: Durable ownership

### One operation owns one established-state tenure

The proposed durable object is `NodeStateTransition`. One object identifies one
immutable transition request for one exact Node name and UID, the exact
predecessor state-owner identity, the requested target and policy identity, the
recovery state retained before mutation, and the witness obligations selected
for that transition class. The plan requires those contracts, not particular
field names or a field-by-field API schema.

The object has two related responsibilities:

- it owns the durable operation record and the transition-evidence reference;
- after confirmation, it owns the tenure during which its target is the
  established state that Runs may consume.

It is not owned by a `ProfilingRun`, and no Run receives an owner reference to
it. Its name is not authority: consumers and successors bind the object UID,
the exact Node UID, the established-state identity, and the admitted transition
evidence identity.

An authorized human operator or a separately approved orchestration controller
may create a `NodeStateTransition` through Kubernetes RBAC. The
`ProfilingRun` reconciler has no create or target-update permission for this
object. The NodeActuator may publish only Node-local actor observations for its
assigned Node. A control-plane confirmer owns witness collection, evidence
admission, current-state selection, and consumer fencing. These write roles must
remain distinct in any implementation RBAC.

A target is immutable. A requested change is a new transition object naming the
exact current object as its predecessor. It is never an update that retargets
the current object. Two successor requests against the same predecessor use a
compare-and-swap current-state claim: one may enter the Node's state lane and
the others end once with a typed `SuccessorConflict` outcome. They do not retry
as competing writers.

### No implicit cleanup by the last Run

Zero bound or executing Runs does not restore the Node and does not delete the
current transition. The object remains the durable owner of the established
state until one of these explicit events occurs:

- an authorized successor transition is accepted;
- a configured tenure deadline causes the state coordinator to create an
  explicit transition to the retained recovery state; or
- fail-closed safety recovery requires an explicit transition to that recovery
  state.

The selected tenure policy and its source identity are frozen before mutation;
they are configuration, not a Go constant. Retirement first closes admission
to new consumers, drains existing consumers, and proves the successor or
restoration transition. Deletion of the current owner follows that proof.
Deleting a Run never requests any of these actions.

Historical transition objects and their immutable evidence remain retained by
the configured record-retention policy after they stop owning the current
state. A historical object cannot become current again; returning to the same
target requires a new transition from the then-current predecessor.

## Checkpoint 2: Per-Node serialization and consumer fencing

### Two serializations with different jobs

The durable state lane and the existing Node Lease are both required:

1. The control-plane state lane admits one current object and at most one
   accepted successor for an exact Node UID. It orders operations, rejects
   competing successor chains, and prevents a state change while consumers are
   executing.
2. The existing `coordination.k8s.io/Lease` keyed by Node UID serializes the
   NodeActuator's Node-local `Observe` and `Apply` work. The NodeActuator
   acquires it before the previous-state observation and retains it through
   target confirmation or through independently proved recovery. Runs never
   acquire or renew this Lease.

The durable lane is not replaced by Lease expiry. If the actor disappears and
its Lease expires, the same operation remains current and blocks every
successor. A replacement NodeActuator may reacquire the Lease only for that
operation's recovery or confirmation. The Lease is not evidence that the state
was reached, held, or restored.

After an operation is confirmed and no recovery is active, the mutation Lease
may be released. The durable lane still prevents another NodeActuator request
from entering a mutation window. Any future adapter must validate both the
exact operation identity admitted to the lane and its Node-UID Lease before
`Apply`.

### Runs form durable reader claims

A Run that binds a confirmed state publishes a durable consumer claim before
its Job can be created. The state owner accepts claims only while it is current,
confirmed, and open to consumers. Job creation then rechecks the same owner UID
and lane fence. A claim that races with a transition into draining cannot create
a Job.

An accepted successor closes the predecessor to new claims and waits until
every claimed Job is terminal. It relists claims after closing the gate; it does
not infer quiescence from an accumulated count in status. Only then may the
successor enter the NodeActuator mutation window. The predecessor records the
successor and the time at which its state tenure ended, so later Run admission
can prove that a completed Job stayed within that tenure.

A transition is never pre-empted by another transition. An established state
is never pre-empted while a claimed Run is executing against it. A fail-closed
safety event may require emergency recovery; any Job whose execution overlaps
that recovery has permanently invalid state evidence and receives a terminal
outcome rather than a retry against the same retained evidence.

When Runs require different states at once, Runs matching the current state may
bind while its gate is open. Runs requiring another state wait without creating
a transition. An authorized successor request closes the current gate, drains
the executing Runs, and then changes state. Waiting Runs bind only when an exact
matching transition becomes confirmed; queue order never weakens exact-state
matching.

## Checkpoint 3: Failure recovery and witness survival

### Unconfirmed means unusable

A partially applied, incompletely observed, or scientifically unadmitted
transition is not an established state. It cannot satisfy a Run precondition.
The state lane remains fenced while the control plane classifies the durable
facts into one of four outcomes:

- no mutation occurred, so the operation ends aborted against a freshly
  observed predecessor state;
- the requested target, required witness facts, and transition evidence are all
  confirmed, so the operation may establish the target;
- mutation may have occurred and the predecessor can be recovered, so the
  NodeActuator restores it and independently reads it back before the operation
  ends recovered; or
- neither target nor recovery can be proved, so the operation ends
  `RecoveryRequired` and the Node remains fenced from CHILL transitions and new
  profiling Jobs.

Apply, retry, and recovery are idempotent projections from the durable
operation, observations, witness record, and Lease fence. Process memory is not
authority. A policy or workload-safety failure before mutation ends without a
restore. The same failure after possible mutation enters the proved recovery
path. A requested restoration is complete only after independent read-back of
the retained recovery state.

### Actor loss does not erase the operation

Actor disappearance never counts as success, failure, reboot, or restoration.
The control-plane witness continues recording facts that remain visible outside
the Node, closes consumer admission, and keeps the state lane fenced. A
replacement NodeActuator resumes the same operation by reacquiring its
Node-UID Lease, independently observing the host, and reconciling that
observation with the retained witness record.

If only the witness remains, the control plane may classify boot continuity or
change and preserve the external measurement record, but it may not synthesize
a Node-local target observation. Confirmation waits for a replacement actor's
read-back. If the actor does not return or the witness and read-back cannot
establish one authorized state, the bounded outcome is `RecoveryRequired` with
no periodic error requeue. Node or operator changes may wake recovery; unrelated
resync does not repeat the failure.

[ADR 0018](../../../adr/0018-state-transition-operation-and-witness-boundary.md)
governs which transitions require an external witness and which instrument
defines an actor-terminating transition window. The object separates actor
observations, control-plane witness observations, and external measurement
references so such a transition is representable. This plan does not enable
one.

## Checkpoint 4: State-precondition confirmation

### Exact requirement, one-time binding

A `ProfilingRun` immutable intent declares the exact Node UID and required
Node-wide state identity. It does not name only a mode label, and it does not
request movement to that state. The requirement is matched against an admitted
current transition; reachability remains a property of the exact transition
and its TPC-mask equivalence class under
[ADR 0017](../../../adr/0017-in-place-power-mode-reachability-boundary.md), not
of the target mode.

Before Job creation, the Run freezes a one-time binding to the matching
transition object UID, its admitted evidence identity, and its state-lane fence.
The binding cannot be silently moved to another object that happens to declare
the same target. A Run with no matching state remains visibly pending and
creates no Job. A conflicting current state also leaves it pending; the Run
does not cause the conflict to be resolved. A declared Run deadline may end
that wait with a typed terminal outcome.

### What `confirmed` means

For Run execution and evidence admission, a state is confirmed only when all of
these contracts hold:

- the transition intent, predecessor, exact Node UID, policy, and target are
  frozen and semantically valid;
- the NodeActuator held the Node-UID Lease, passed fail-closed workload safety,
  and supplied independent previous and target observations with the required
  settling result;
- the control-plane and external witness obligations selected for the
  transition class are complete;
- the transition evidence loader and admitter returned an admitted typed
  result; and
- the object is the current state-lane owner and is open to the Run's durable
  consumer claim.

A display condition alone is not confirmation. The confirmer constructs the
confirmed-state binding from the immutable request, admitted transition
evidence, current object UID, and lane fence. No other component can construct
that binding from status text.

The binding remains valid for a completed Run if its Job interval is wholly
inside the recorded established-state tenure, even when a successor is
confirmed before Run evidence is finalized. This permits delayed evidence
processing without keeping the Node in the old state.

## Checkpoint 5: Transition and Run evidence admission

### The transition's evidence

The immutable transition-evidence object contains enough information to audit
one operation without consulting a Run:

- transition, predecessor, Node, target, policy, actor, Lease-fence, and
  witness identities;
- fail-closed workload-safety observations;
- actor observations and lifecycle timestamps for the predecessor, requested
  target, settling, and any recovery, including the existing boot identity on
  each actuation observation;
- control-plane witness observations and, when required by the transition
  class, an immutable external wall-power trace reference and its measurement
  window;
- the typed operation outcome and any admitted transition duration, transition
  energy, and survival result required by the frozen measurement policy; and
- references to raw inputs and exclusions rather than copied raw samples.

The transition record in Kubernetes retains the immutable intent, compact
outcome, and immutable evidence-store reference. Full raw observations and the
admitted record live at the evidence boundary; mutable status is not the
scientific record.

### Admit once, consume by reference

The transition loader validates structure and semantics once and returns a type
that only this admission boundary can construct. Admission checks exact object
and Node identity, the frozen policy, time ordering, independent read-backs,
the applicable witness obligations, the mutation/recovery outcome, and the
authorized transition edge under ADR 0017. It does not use canonical
re-encoding or byte equality as a validation gate.

A permanent validation failure produces one terminal transition-evidence
outcome and no error requeue. A transient store or witness read remains
retryable. The target cannot become confirmed before transition evidence is
admitted.

Run execution evidence references the bound transition object UID, admitted
transition-evidence identity, and state-lane fence. Run admission checks that
the binding matches the immutable Run requirement, that the Job interval falls
within the owner's established tenure, and that no recovery or successor
mutation overlapped it. It does not reload the transition's raw trace, rerun the
reachability decision, recompute transition quantities, or compare encodings.

Transition energy is not charged to every Run that references the state. A
higher-level campaign may account for one transition across its consumers, but
that allocation and any scheduler use require their own approved design.

## Checkpoint 6: Stored-schema compatibility

### The observed hazard is a release gate

The compatibility design is required by three observed strict-schema failure
classes: `NodeActuation` objects without a newly required policy field;
`ProfilingRun` and `BatchSaturation` objects whose retained evidence used a
superseded schema; and `NodeActuation` objects without the required boot-identity
field. Each class entered an unbounded reconcile error loop, and one class also
kept deletion blocked behind its finalizer.

`NodeStateTransition` may not repeat that behavior. Its first storage contract
must include a stable lifecycle envelope and recovery capsule. The envelope
keeps the object and Node identities, contract version, compatibility outcome,
mutation fence, and finalizer protocol readable. The recovery capsule keeps the
minimum previous-state and policy facts needed to decide whether mutation was
possible and to perform and prove recovery. Versioned scientific evidence is
referenced outside that stable recovery contract.

The schema-evolution rules are:

1. No rollout adds a required field or changes a stored field's type in place
   for an already served storage version. Current writers enforce current
   creation semantics without making retained objects undecodable.
2. A change that needs new required meaning uses a new served API version or a
   new evidence version. Compatible conversion preserves facts; it never
   invents a policy, boot identity, observation, or witness.
3. Reader support and an inventory of all stored versions deploy before a new
   writer. An old reader is removed only after every old object is migrated,
   safely retired, or placed in the defined compatibility outcome.
4. Unsupported evidence is a permanent typed admission result, not an ordinary
   reconcile error. Unsupported API objects are handled by an unstructured
   compatibility path that can read the stable envelope even when the current
   typed controller cannot decode the versioned body.

An object the normal controller cannot decode is marked once as
`UnsupportedStoredSchema`, closed to consumers, and fenced from mutation. The
compatibility path returns success without a periodic requeue and emits one
operator-visible event. It may remove the finalizer immediately when the stable
mutation fence proves no mutation began. If mutation may have begun, it uses the
stable recovery capsule, Node-UID Lease, NodeActuator, and independent read-back
to prove the retained recovery state before removing the finalizer. If even the
recovery capsule is absent or corrupt, it records `RecoveryRequired`, retains
the per-Node fence, and waits for an explicit operator-supplied recovery proof;
it does not loop or silently abandon restoration.

CRD removal is forbidden while any object of any served version still has the
recovery finalizer or owns a Node's state lane. A release gate must exercise
stored fixtures for every supported and unsupported version, including a
deleting object whose versioned body is not understood.

## Checkpoint 7: Migration from Run-owned actuation

Migration is a drain-and-cutover, not adoption of a Run-owned handoff as an
established shared state:

1. Inventory every `NodeActuation`, its owning Run, phase, finalizer, retained
   previous state, evidence version, and exact Node UID. Install compatibility
   readers before changing any strict schema.
2. Keep the Run-owned Profiler and NodeActuator controllers available while
   every handoff that may have mutated a Node reaches independently proved
   restoration or a visible `RecoveryRequired` fence. No new-path transition
   may enter that Node's lane during this drain.
3. Stop creating new Run-owned handoffs. Runs already bound to one finish under
   the Run-owned lifecycle; they are not rebound in place. Fixed-state Runs are
   unaffected.
4. Enable the transition path per exact Node UID only after that Node has no
   unfinished legacy handoff or blocked legacy finalizer. Its first transition
   starts from a fresh NodeActuator observation and a fresh witness record; it
   does not infer an established state from an old Run or handoff.
5. Remove the legacy creation and reconciliation path only after the inventory
   is empty of active handoffs and every deletion gate passes.

A legacy `NodeActuation` that is proved restored remains a historical Run-owned
record until its retention policy deletes it. Its existing Run evidence keeps
the semantics of the schema under which it was admitted. It is never converted
into a `NodeStateTransition`, never used as current-state authority, and never
has missing boot or policy facts synthesized.

A legacy object with an unsupported stored shape enters the same bounded
compatibility classification. If its stable facts prove that mutation did not
begin, deletion may finish. If it may have mutated and retains enough previous
state, a compatibility recovery performs and proves restoration before
finalizer removal. Otherwise the exact Node remains `RecoveryRequired` until an
operator supplies independently observed recovery proof. Unsupported retained
Run or campaign evidence receives its permanent terminal outcome; it is not
rewritten to the current evidence schema.

No controller dual-writes a Run-owned `NodeActuation` and a
`NodeStateTransition` for the same action. The shared Node-UID Lease remains the
last mutation mutex during the cutover, while the per-Node migration gate
prevents legacy and transition state machines from alternating ownership.

## Verification boundary for an implementation

Approval of this design would not by itself prove the control path. An
implementation would require, at minimum:

- pure decision tests for predecessor chaining, successor conflicts, consumer
  drain, actor loss, partial mutation, recovery, and no-preemption;
- controller tests proving that Run creation never creates a transition, Job
  creation requires the one-time confirmed-state binding, and a successor
  cannot mutate before all claimed Jobs are terminal;
- evidence tests proving one-time transition admission, reference-only Run
  consumption, interval fencing, and permanent invalid-evidence outcomes;
- envtest coverage for actor restart, Lease expiry and reacquisition, competing
  states, deletion during recovery, and migration coexistence;
- stored-object fixtures for every supported old version and undecodable-body,
  missing-policy, missing-boot-identity, superseded-evidence, and finalizer
  deletion cases; and
- focused live in-place apply, witness, multi-Run consumption, successor, and
  proved restoration evidence on an explicitly authorized test Node.

Live validation would cover only an ADR-0017-authorized in-place transition.
It would not prove or exercise reboot capability.

## Reboot-shaped, not reboot-enabled

The actor/witness split, stable recovery capsule, boot-identity observations,
external measurement reference, resumable operation identity, and
`RecoveryRequired` fence exist so an actor-terminating transition can be
described without changing the ownership model.

No reboot mechanism, privilege, authorization, drain policy, recovery timeout,
or shared-Node blast-radius policy is selected here. A separate accepted ADR
and plan would have to authorize those decisions, define the exact executable
transition class, and supply safety and live evidence before any reboot-class
request could pass validation. Until then, such a request is permanently
unsupported before mutation.

## Not authorized by this plan

- any API, CRD, Go package, controller, RBAC, chart, values, or deployment
  change
- any reboot command, reboot privilege, or executable reboot-class transition
- a change to ADR 0017's transition-and-TPC-equivalence-class reachability rule
- a Run-created, Run-owned, Run-held, or Run-restored Node state
- pre-emption of an executing Run by a competing requested state
- reinterpretation, rewriting, or silent defaulting of retained evidence or
  durable objects
- transition-cost allocation to profiles, a `DeviceProfile` API, scheduler
  consumption, or scheduler policy
