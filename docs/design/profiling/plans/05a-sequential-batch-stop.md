# Plan 5a: Sequential Batch Sweep with Early Stopping

> Navigation: [Profiling design index](../README.md)

## Status

Completed derivation plan. The pure family-contract, family-verification,
exact-state curve, and sequential-stop constructors are implemented in
`internal/profilederivation`, with golden tests for every stop reason and the
recovery/accumulation cases. This plan answers the same question as
[Plan 5](05-batch-saturation-campaign.md) at a lower evidence strength and a
much lower measurement cost. It produces a decision, not a statistical claim.

This plan owns the stopping rule only. The control path that produces and
composes the Runs it consumes belongs to
[Plan 7](07-batch-saturation-control-path.md), which selects this rule by name
as one measurement method. This plan authorizes no API, intent schema, or
policy-catalog change of its own.

The two plans compose rather than compete. This one is the default path, and its
output is exactly the candidate Plan 5 would confirm.

[ADR 0014](../../../adr/0014-fixed-artifact-curve-composition.md) closes the
identity decision for a runtime that uses one immutable artifact per fixed
batch. Exact point identities remain unchanged; the sweep consumes one frozen
curve family and one admitted idle baseline per distinct member state. The
RKNN batch-one and batch-two members now have aligned retained L3 evidence under
the same catalog, recipe, runtime, and helper-image generation. Those diagnostic
Runs preceded a committed `CurveFamilyName`, so they validate the
input boundary but are not retroactive Plan 5a campaign evidence. The new
pre-registered campaign produced those fresh Runs and closed the L4 gate.

## Question

> Walking batch sizes upward, when has energy per item stopped improving enough
> to keep going?

## What this plan does

Training loops face the same shape of problem. A model trains epoch by epoch, a
validation metric improves, then flattens, and continuing costs time for nothing.
Every major training framework solves it the same way: watch the metric, keep the
best value seen so far, count how many steps in a row failed to beat it by a
meaningful margin, and stop when that count reaches a limit. Report the best
step, not the step where you stopped.

A batch sweep has the same shape. Batch size plays the role of the epoch counter,
and incremental energy per item plays the role of the validation loss. Larger
batches usually cost less energy per item, then flatten, and continuing to
measure costs cluster time for nothing.

So this plan takes the early-stopping rule as it is actually implemented in those
frameworks, adapts the two things that differ for energy measurement, and runs it
over batch sizes. It does not preselect a statistical `Kmax` or stopping batch.
ADR 0014 still requires the catalog-resolved eligible member map to be frozen
before measurement; exhausting that map is a typed capability boundary.

## Where the rule comes from

Five widely used implementations agree on the same core. The elements below are
taken from their source, not paraphrased from documentation.

**Keras `EarlyStopping`** — `keras/src/callbacks/early_stopping.py`, with the
comparison in `MonitorCallback`:

```python
def _is_improvement(self, monitor_value, reference_value):
    if reference_value is None:
        return True
    return self.monitor_op(monitor_value - self.min_delta, reference_value)

# in on_epoch_end
self.wait += 1
if self._is_improvement(current, self.best):
    self.best = current
    self.best_epoch = epoch
    ...
    self.wait = 0
    return
if self.wait >= self.patience and epoch > 0:
    self.stopped_epoch = epoch
    self.model.stop_training = True
```

For a minimized metric `min_delta` is negated, so the test reads
`current + |min_delta| < best`.

**PyTorch Lightning `EarlyStopping`** —
`src/lightning/pytorch/callbacks/early_stopping.py`:

```python
elif self.monitor_op(current - self.min_delta, self.best_score.to(current.device)):
    should_stop = False
    self.best_score = current
    self.wait_count = 0
else:
    self.wait_count += 1
    if self.wait_count >= self.patience:
        should_stop = True
        self.stopping_reason = EarlyStoppingReason.PATIENCE_EXHAUSTED
```

preceded by separate typed exits for a non-finite metric, a
`stopping_threshold` that is already good enough, and a `divergence_threshold`
that is beyond saving.

**scikit-learn** — `_update_no_improvement_count` in
`neural_network/_multilayer_perceptron.py`:

```python
if self.loss_curve_[-1] > self.best_loss_ - self.tol:
    self._no_improvement_count += 1
else:
    self._no_improvement_count = 0
if self.loss_curve_[-1] < self.best_loss_:
    self.best_loss_ = self.loss_curve_[-1]
```

**Hugging Face `EarlyStoppingCallback`** — `check_metric_value`:

```python
if state.best_metric is None or (
    operator(metric_value, state.best_metric)
    and abs(metric_value - state.best_metric) > self.early_stopping_threshold
):
    self.early_stopping_patience_counter = 0
else:
    self.early_stopping_patience_counter += 1
```

**Optuna `PatientPruner`** — a window form of the same idea:

```python
if steps.size <= self._patience + 1:
    return False
maybe_prune = np.nanmin(scores_before_patience) + self._min_delta < np.nanmin(
    scores_after_patience
)
```

### What they agree on

| Element | Keras | Lightning | scikit-learn | HF | Optuna |
|---|---|---|---|---|---|
| compare against the best value so far | yes | yes | yes | yes | yes (window minimum) |
| improvement must exceed a margin | `min_delta` | `min_delta` | `tol` | `early_stopping_threshold` | `min_delta` |
| count consecutive failures to improve | `wait` | `wait_count` | `_no_improvement_count` | `..._patience_counter` | window length |
| reset the count on improvement | yes | yes | yes | yes | implicit |
| stop at a fixed count | `patience` | `patience` | `n_iter_no_change` | `..._patience` | `patience` |
| report the best step, not the last | `best_epoch` | `best_score` | `best_loss_` | `best_metric` | — |
| a grace region before the rule engages | `start_from_epoch` | — | — | — | `steps <= patience+1` |
| a confidence statement anywhere | no | no | no | no | no |

Two of these matter most for a batch sweep.

**Comparison is against the best value so far, not against the previous step.**
A single bad step does not move the reference and does not end the sweep. This is
what makes the rule safe on a curve that is not monotonic — a batch size where
the runtime picks a poor kernel is survivable, and the sweep continues from the
same reference.

**The reference does not move on a non-improving step.** So the step after it is
compared against the same older reference, and its improvement is measured
cumulatively from there. Several small gains that individually miss the margin
can together exceed it and reset the count. `patience` is exactly the number of
steps over which that accumulation is allowed.

## The rule

Two things differ for energy measurement, and both are stated as deviations.

**The margin is relative, not absolute.** All five implementations compare an
absolute difference. CHILL's output is a catalog covering models whose energy per
item differs by orders of magnitude, so an absolute margin would need a separate
value per model. The improvement test becomes:

```text
improvement  iff  epsilon(b) < best * (1 - min_delta)
```

with `min_delta` a fraction.

**Each step is an average of repeated trials.** A training epoch produces one
loss. A batch step produces `n` admitted trials, and `epsilon(b)` is their mean,
computed exactly as Plan 3 defines it, against one shared idle baseline. This
changes nothing in the rule; it only defines the value the rule sees.

The resulting sweep:

```text
best        = none
best_batch  = none
wait        = 0

for b = 1, 2, 3, ...:
    measure n trials at batch b
    if no trial at b is admissible:            stop, CapabilityBoundary
    epsilon_b = mean incremental energy per item over the admitted trials

    if best is none or epsilon_b < best * (1 - min_delta):
        best       = epsilon_b
        best_batch = b
        wait       = 0
        continue

    wait = wait + 1
    if wait >= patience:                       stop, NoFurtherImprovement
    if epsilon_b > best * divergence_factor:   stop, EnergyDivergence

report best_batch
```

`divergence_factor` is Lightning's `divergence_threshold` in relative form: a
batch whose energy per item is far above the best seen is not going to recover,
and continuing wastes cluster time.

### A worked sweep

Take `min_delta = 5%` and `patience = 2`, on a curve that flattens:

| batch | `epsilon` (J/item) | test against | improvement | `wait` | `best_batch` |
|---:|---:|---|---|---:|---:|
| 1 | 0.0666 | — | first value | 0 | 1 |
| 2 | 0.0410 | `0.0666 x 0.95 = 0.0633` | yes | 0 | 2 |
| 3 | 0.0344 | `0.0410 x 0.95 = 0.0390` | yes | 0 | 3 |
| 4 | 0.0331 | `0.0344 x 0.95 = 0.0327` | no, 3.8% gain | 1 | 3 |
| 5 | 0.0327 | `0.0344 x 0.95 = 0.0327` | no, 4.9% gain | 2 | 3 |

`wait` reaches `patience` at batch 5. The sweep stops and reports batch 3, having
measured five batch sizes.

Note what row 5 shows. Because `best` did not move at batch 4, batch 5 is
compared against batch 3's value, and its cumulative gain of 4.9% is nearly
enough to reset the count. Had the curve given one more step of the same size, or
had `patience` been 3, the sweep would have continued and moved `best` forward.
This is the accumulation described above, and it is the reason `patience` is a
substantive parameter rather than a convenience.

### Cost

The sweep measures `best_batch + patience` batch sizes, at `n` trials each. The
worked example above cost 5 batch sizes. At three trials per batch that is 15
active Runs, plus one idle baseline set per distinct member state. A
dynamic-batch artifact still needs only one set.

No statistical `Kmax` is selected. The sweep discovers its stopping point within
the frozen eligible member map, or reports exhaustion of that map as
`CapabilityBoundary`.

## Implementation status

| Boundary | Status |
|---|---|
| frozen family contract and strict persisted-contract decoder | implemented |
| post-Loader plain member verification and verified family construction | implemented with no Kubernetes dependency |
| exact-state baseline ownership and pass/trial uniqueness | implemented |
| relative best-so-far rule and typed stop reasons | implemented and golden-tested |
| first policy/family/inclusion record | frozen before measurement and preserved |
| live sequential result | completed; selected batch 1, `CapabilityBoundary` at batch 3 |

## What this plan reports

```text
SelectedBatch      = 3        the batch with the best admitted energy per item
StopReason         = NoFurtherImprovement | CapabilityBoundary | EnergyDivergence
MeasuredBatches    = 1..5     every batch actually measured
MinDelta           = 0.05
Patience           = 2
TrialsPerBatch     = 3
EpsilonByBatch     = the mean at every measured batch
CurveFamilyName = the pre-frozen eligible member map
ObservedEnvironment = the Loader-verified shared environment
IdleBaselinesByState = every exact baseline used by the measured prefix
```

Every measured batch is reported, including the ones that failed to improve. A
reader can re-apply the rule to the reported values and reach the same batch.

## What this plan does not claim

The rule produces a decision about where to stop measuring. It attaches no
confidence to that decision, and neither does any of the implementations it comes
from.

Three consequences follow, and each belongs in whatever consumes the result.

**Trial noise can stop the sweep early.** The rule compares means. With three
trials and the trial-to-trial spread of 2.85% observed at batch one, the mean at
each batch carries roughly 1.6% of its own value in noise, so a step whose true
gain is close to the margin can be measured on either side of it. Raising `n`
narrows that; raising `patience` requires the mis-measurement to happen twice in
a row. Neither turns the result into a confidence statement.

**The rule is step-wise, not cumulative.** It stops when no further step gains
the margin over the running best. A curve that keeps giving small gains
indefinitely can therefore be stopped before those gains add up to something
larger. `patience` bounds how much of that accumulation the rule can see, as the
worked sweep shows. Plan 5's quantity asks the cumulative question directly and
answers it with a stated confidence.

**The result is not minimal at confidence.** `best_batch` is the batch with the
lowest measured mean that beat its predecessor by the margin. No statement is
made that a smaller batch would have failed the same test.

## Relationship to Plan 5

Both plans answer when batching stops paying off. They differ in what they
produce and what they cost.

| | Plan 5a | Plan 5 |
|---|---|---|
| output | a decision: stop at this batch | a claim with a stated confidence |
| batch domain | discovered by the sweep | declared as `1..Kmax` before starting |
| passes | one | discovery, then confirmation on fresh Runs |
| confidence construction | none | one bound covering every larger batch at once |
| illustrative Run count | 15 | 42 to 90 |
| depends on the rebuilt estimator | no, means only | yes, intervals |

Use Plan 5a to choose an operating batch and to populate a profile catalog. Use
Plan 5 when the batch has to be published as a statistical result. Running 5a
first costs nothing in either case, because `SelectedBatch` is the candidate that
Plan 5's confirmation pass takes as its input.

## Code boundary

```text
internal/profilederivation/   the stopping rule over a sequence of batch means
```

The rule consumes per-batch means only after ADR 0014's family constructor has
matched every point to its exact-state baseline. It does not use confidence
intervals, so its stopping comparison is unaffected by the interval
reconstruction that Plan 5 requires. Nothing changes in
`api/v1alpha1`, the intent schema, the Profiler controller, the measurement
worker, or single-Run admission.

## Explicitly out of scope

- any statement of confidence, of how often the result would be right, or of an
  error rate
- a declared batch domain, a confirmation pass, or a second policy version
- the interval reconstruction Plan 5 specifies
- `DeviceProfile` or `ClusterEnergyModel` schema, persistence, or status
- a campaign CRD, controller, or orchestrator
- any intent schema change
- non-consecutive batch steps
- cross-node or cross-model pooling
- throughput, latency, or SLO feasibility derivation
- scheduler or enumerator consumption

## Acceptance boundary

Plan 5a is complete when:

- `min_delta`, `patience`, `n`, and `divergence_factor` are fixed before the
  sweep starts and are reported with the result;
- every batch belongs to the frozen family, uses its exact-state idle baseline,
  and the sweep names the family and all baselines in its output;
- golden sequences close the no-further-improvement, capability-boundary, and
  energy-divergence stops;
- a sequence with a single worse batch followed by a recovering one does not stop
  at the worse batch, provided `patience` allows it;
- a sequence of sub-margin gains that together exceed the margin within
  `patience` steps resets the counter and continues;
- the reported batch is the best-scoring one rather than the last measured one;
  and
- re-applying the rule to the reported per-batch means reproduces the reported
  batch.

## Implementation sequence

1. Fix `min_delta`, `patience`, `n`, and `divergence_factor`, and the rule that a
   batch with no admissible trial is a capability boundary rather than a
   non-improving step.
2. Implement ADR 0014's frozen family projection and exact-state baseline
   ownership in the pure derivation package.
3. Implement the rule over a sequence of per-batch means, with the improvement
   test in its relative form and the reference updated only on improvement.
4. Add the typed stop reasons and the reported fields.
5. Add golden sequences for each stop reason and for the non-monotonic and
   accumulating cases.
6. Run the sweep on the live path and record the measured batches, the per-batch
   means, and the selected batch.

## Follow-on boundary

If the selected batch has to carry a stated confidence, it becomes the input
candidate to [Plan 5](05-batch-saturation-campaign.md)'s confirmation pass, and
no measurement from this sweep is wasted.

Publishing the selected batch as a `DeviceProfile`, and any controller that runs
the sweep unattended, belong to a later plan.
