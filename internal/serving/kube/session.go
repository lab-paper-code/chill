package kube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	core "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	sessionKind = "measurement-session"
	// SessionResultPrefix holds one result object per session,
	// batch-energy-optimizations/<UID>/result.json.
	SessionResultPrefix = "batch-energy-optimizations/"

	refusedSessionMissing    = "SessionSettingsMissing"
	refusedSessionUnresolved = "SessionUnresolved"

	sessionConditionComplete = "MeasurementComplete"
	sessionOutcomeFailed     = "Failed"
	// sessionOutcomeMeasured is the outcome of a session that measured every
	// batch size of the path.
	sessionOutcomeMeasured = "AllBatchesMeasured"

	// Level end reasons of chill-server session-load.
	sessionSamplesReached         = "SamplesReached"
	sessionPredictedOverLimit     = "PredictedDurationExceedsLimit"
	sessionServiceOverLifetime    = "BatchServiceExceedsLifetime"
	sessionAllocationBeforeBatch  = "AllocationFailedBeforeAnyBatch"
	sessionRequestLifetimeExpired = "RequestLifetimeExpired"
)

// SessionOperationCostPrefix holds a session's operation-cost records, whose
// operation IDs are <UID>.<step>.<name> (sessionRun.measure). The dot ends the
// UID, so one session's prefix never matches another's.
func SessionOperationCostPrefix(nodeUID string, uid types.UID) string {
	return servingops.OperationMeasurementPrefix + nodeUID + "/" + string(uid) + "."
}

// sessionPlan is what admission resolved for a granted session.
type sessionPlan struct {
	preparation    SessionPreparation
	meter          servingops.MeterDeclaration
	minimumSamples int
	confidence     float64
	// powered is set when the Node already reads back the session's mode;
	// otherwise maxActuation bounds the switch into it.
	powered      bool
	maxActuation time.Duration
}

type sessionNativeLevel struct {
	rows   []profilederivation.NativeBatchRow
	inputs int
}

// sessionReads are the observation records a session derives its result from.
type sessionReads struct {
	// native returns batch rows grouped by level identifier, selected with
	// one batch-size prefix over receipt hours since the session started.
	native func(ctx context.Context, workloadPrefix string, since time.Time, excluded map[string]bool) (map[string]sessionNativeLevel, error)
	power  func(ctx context.Context, meter servingops.MeterDeclaration, from, to time.Time) ([]profilederivation.Sample, error)
	memory func(ctx context.Context, podUID string, from, to time.Time) (observation.PodMemoryPeak, error)
}

// observedSessionReads reads the serving observation log.
func observedSessionReads(log *observation.ObservationLog) sessionReads {
	return sessionReads{
		native: func(ctx context.Context, workloadPrefix string, since time.Time, excluded map[string]bool) (map[string]sessionNativeLevel, error) {
			joins := map[string]*serving.NativeBatchJoin{}
			err := log.ScanSessionRows(ctx, workloadPrefix, since, time.Now(), func(row serving.RequestRow) error {
				if excluded[row.WorkloadUID] {
					return nil
				}
				join := joins[row.WorkloadUID]
				if join == nil {
					join = serving.NewNativeBatchJoin()
					joins[row.WorkloadUID] = join
				}
				join.Add(row)
				return nil
			})
			if err != nil {
				return nil, err
			}
			rows := make(map[string]sessionNativeLevel, len(joins))
			for uid, join := range joins {
				batches, err := join.Batches()
				if err != nil {
					return nil, err
				}
				rows[uid] = sessionNativeLevel{rows: batches, inputs: join.CompletedInputs()}
			}
			return rows, nil
		},
		power: func(ctx context.Context, meter servingops.MeterDeclaration, from, to time.Time) ([]profilederivation.Sample, error) {
			read, err := log.PowerSamples(ctx, observation.OperatingPowerMeter{Node: meter.Node, Meter: meter.ID, Scope: meter.Scope, Source: meter.Source}, from, to)
			samples := make([]profilederivation.Sample, len(read.Samples))
			for i, sample := range read.Samples {
				samples[i] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
			}
			return samples, err
		},
		memory: log.PodMemoryPeak,
	}
}

// sessionRefusal judges a measurement session before its grant. A refused
// session is never granted, so no session is left without its run.
func (c *SchedulerController) sessionRefusal(ctx context.Context, beo *edge.BatchEnergyOptimization, node *core.Node, in admissionInputs) (plan sessionPlan, reason, detail string, wait bool) {
	settings := beo.Spec.Session
	if settings == nil {
		return plan, refusedSessionMissing, "measurement request has no session settings", false
	}
	if in.config.Session.NodeName == "" {
		return plan, refusedSessionUnresolved, "measurement session requires Session.NodeName for the ChillServer load generator", false
	}
	if node.Name == in.config.Session.NodeName {
		return plan, refusedSessionUnresolved, "the load generator runs on this Node, so its power would be measured as the model's", false
	}
	i := slices.IndexFunc(in.config.Meters, func(meter servingops.MeterDeclaration) bool { return meter.Node.UID == string(node.UID) })
	if i < 0 {
		return plan, MeterUndeclared, node.Name, false
	}
	plan.meter = in.config.Meters[i]
	confidence, err := settings.Confidence()
	if err != nil {
		return plan, refusedSessionUnresolved, err.Error(), false
	}
	plan.confidence = confidence
	if in.config.Session.Prepare == nil {
		return plan, refusedSessionUnresolved, "ChillServer has no Profiler facts to resolve a measurement session", false
	}
	if plan.preparation, err = in.config.Session.Prepare(ctx, beo); err != nil {
		var retry *SessionResolutionRetry
		if errors.As(err, &retry) {
			return plan, "", firstLine(err.Error()), true
		}
		return plan, refusedSessionUnresolved, firstLine(err.Error()), false
	}
	if len(plan.preparation.BatchSizes) == 0 {
		return plan, refusedSessionUnresolved, "measurement session resolved no batch size", false
	}
	if plan.minimumSamples, err = native.MinimumServiceSamples(plan.preparation.SLOPercentile); err != nil {
		return plan, refusedSessionUnresolved, err.Error(), false
	}
	mode := plan.preparation.RunConfig.State.PowerMode
	plan.powered = servingstate.ReadBackMode(node, servingstate.NodeClass(node, in.classes)) == mode
	maxActuation, ok := nodePowerTargets(node, in.classes, in.config.PowerPolicies)[mode]
	if !plan.powered && !ok {
		return plan, refusedPowerModeUnknown, mode, false
	}
	plan.maxActuation = maxActuation
	return plan, "", "", false
}

// startSession registers the run of a granted measurement session.
func (c *SchedulerController) startSession(node *core.Node, beo *edge.BatchEnergyOptimization, plan sessionPlan, config SchedulerControllerConfig) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	uid := string(beo.UID)
	root := &measurementRoot{object: beo, node: node.Name}
	operation := servingstate.Operation{ID: sessionKind + ":" + node.Name + ":" + uid, Kind: sessionKind, Node: ref, Holder: uid}
	c.startNodeRun(operation, config, func(ctx context.Context, result *servingstate.OperationResult) error {
		err := c.runSession(ctx, beo, ref, plan, config)
		return errors.Join(err, c.handBack(ctx, ref, uid, root, false, result))
	})
}

// setSessionStatus writes a session's phase, outcome and MeasurementComplete
// condition for the current generation.
func setSessionStatus(beo *edge.BatchEnergyOptimization, phase edge.MeasurementPhase, outcome, message string, result *edge.ProfilingRunEvidenceReference, batches int) {
	beo.Status.Phase, beo.Status.ObservedGeneration, beo.Status.Outcome = string(phase), beo.Generation, outcome
	beo.Status.Result, beo.Status.CompletedBatches = result, int32(batches)
	if phase == edge.MeasurementPhaseMeasuring {
		apimeta.RemoveStatusCondition(&beo.Status.Conditions, sessionConditionComplete)
		return
	}
	status := meta.ConditionFalse
	if phase == edge.MeasurementPhaseComplete {
		status = meta.ConditionTrue
	}
	apimeta.SetStatusCondition(&beo.Status.Conditions, meta.Condition{Type: sessionConditionComplete, Status: status,
		Reason: outcome, Message: message, ObservedGeneration: beo.Generation})
}

func setSessionPending(beo *edge.BatchEnergyOptimization, message string) {
	apimeta.SetStatusCondition(&beo.Status.Conditions, meta.Condition{Type: sessionConditionComplete, Status: meta.ConditionUnknown,
		Reason: "SessionResolutionPending", Message: message, ObservedGeneration: beo.Generation})
}

// sessionRun is one session's progress.
type sessionRun struct {
	c         *SchedulerController
	beo       *edge.BatchEnergyOptimization
	ref       serving.NodeReference
	plan      sessionPlan
	config    SchedulerControllerConfig
	base      servingops.MeasurementRequest
	steps     int
	costError error
	// started precedes the session's first operation.
	started time.Time
}

// measure retains operation cost independently of the physical effect needed
// by the session. A missing power window must not skip the action.
func (r *sessionRun) measure(ctx context.Context, name string, operation servingstate.Operation) (servingops.OperationMeasurement, string, error) {
	request := r.base
	operation.ID, operation.Node = fmt.Sprintf("%s.%d.%s", r.beo.UID, r.steps, name), r.ref
	r.steps++
	request.Operation = operation
	record, receipt, err := r.c.measureStep(ctx, r.config, request)
	key := receipt.Object.Key
	var costSaveErr error
	if key == "" {
		costSaveErr = err
	}
	if record.Result.StartedAt.IsZero() {
		reason := record.Reason
		if reason == "" && err != nil {
			reason = err.Error()
		}
		record.Reason = "unmeasured action: measurement did not invoke operation"
		if reason != "" {
			record.Reason += ": " + reason
		}
		if key != "" {
			operation.ID += ".unmeasured"
		}
		actionCtx, cancel := context.WithTimeout(ctx, request.OperationBudget)
		record.Result, err = r.c.measurement.Execute(actionCtx, operation)
		cancel()
		record.SchemaVersion, record.Verdict = servingops.OperationMeasurementSchema, servingops.MeasurementInsufficient
		if record.Request.Meter.ID == "" {
			record.Request.Meter = r.plan.meter
		}
		record.Request.Operation, record.Request.Store = operation, r.config.EvidenceStore
		raw, encodeErr := json.Marshal(record)
		costSaveErr = errors.Join(costSaveErr, encodeErr)
		if encodeErr == nil && r.config.Evidence != nil {
			writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			identity := evidencestore.ObjectIdentity{Store: r.config.EvidenceStore, Key: servingops.OperationMeasurementKey(operation), Size: int64(len(raw))}
			if _, writeErr := r.config.Evidence.PutImmutable(writeCtx, identity, bytes.NewReader(raw)); writeErr == nil {
				key = identity.Key
				costSaveErr = nil
			} else {
				costSaveErr = errors.Join(costSaveErr, writeErr)
			}
			cancelWrite()
		}
		if r.config.Evidence == nil {
			costSaveErr = errors.Join(costSaveErr, errors.New("operation measurement evidence store is not configured"))
		}
	}
	if costSaveErr != nil {
		r.costError = errors.Join(r.costError, fmt.Errorf("%s: %s: %w", operation.ID, record.Reason, costSaveErr))
	}
	switch operation.Kind {
	case servingstate.OperationPrepare:
		instance := schedulerPhysicalState(operation.Prepare.Instance.ID, record.Result.Actual.Instances)
		if instance != nil && instance.PodUID != "" && record.Result.Actual.Native[instance.ID].Ready {
			return record, key, nil
		}
		if err == nil && record.Reason == "" {
			batch := operation.Prepare.Config.Policy.FixedBatchSize
			switch {
			case instance == nil || instance.PodUID == "":
				err = fmt.Errorf("prepare of batch %d left no instance", batch)
			case record.Result.Actual.Native == nil:
				err = fmt.Errorf("batch %d instance reported no runtime metadata", batch)
			default:
				err = fmt.Errorf("prepare of batch %d left no ready instance", batch)
			}
		}
	case servingstate.OperationRemove:
		present := slices.ContainsFunc(record.Result.Actual.Pods, func(pod workloadobserver.Pod) bool { return pod.UID == operation.PodUID })
		if record.Result.Status == servingstate.NativeOperationComplete && !present {
			return record, key, nil
		}
	case servingstate.OperationPower:
		if record.Result.Actual.PowerMode == operation.PowerMode {
			return record, key, nil
		}
	}
	if record.Result.Error != "" {
		err = errors.New(record.Result.Error)
	}
	if err == nil {
		err = errors.New(record.Reason)
	}
	return record, key, err
}

// removePrepared removes only the exact Pod returned by this preparation.
// Cleanup survives cancellation and remains bounded by the operation window.
func (r *sessionRun) removePrepared(ctx context.Context, name string, spec servingstate.PrepareSpec, prepared servingops.OperationMeasurement) (string, error) {
	operation := servingstate.Operation{Kind: servingstate.OperationRemove}
	if evidence := prepared.Result.Preparation; evidence != nil && evidence.PodUID != "" {
		operation.PodUID = evidence.PodUID
	} else if instance := schedulerPhysicalState(spec.Instance.ID, prepared.Result.Actual.Instances); instance != nil {
		operation.PodUID = instance.PodUID
	}
	if operation.PodUID == "" {
		return "", nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.base.Sampling.Duration)
	defer cancel()
	_, key, err := r.measure(cleanupCtx, name, operation)
	return key, err
}

// runSession measures the batch sizes in increasing order, derives the
// result, stores it and writes the status. A stop at a batch size after the
// first ends the session with the batches before it; at the first, or on any
// other failure, the session fails with the original sentence.
func (c *SchedulerController) runSession(ctx context.Context, beo *edge.BatchEnergyOptimization, ref serving.NodeReference, plan sessionPlan, config SchedulerControllerConfig) error {
	var run *sessionRun
	finish := func(phase edge.MeasurementPhase, outcome, message string, result *edge.ProfilingRunEvidenceReference, batches int) error {
		// The final status survives a canceled run; publication is bounded on its own.
		statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return c.patchMeasurementStatus(statusCtx, beo, func() {
			setSessionStatus(beo, phase, outcome, message, result, batches)
			if run != nil && run.costError != nil {
				apimeta.SetStatusCondition(&beo.Status.Conditions, meta.Condition{Type: "OperationCostEvidence", Status: meta.ConditionFalse,
					Reason: "CostEvidenceNotStored", Message: run.costError.Error(), ObservedGeneration: beo.Generation})
			}
		})
	}
	fail := func(err error) error {
		return errors.Join(err, finish(edge.MeasurementPhaseFailed, sessionOutcomeFailed, firstLine(err.Error()), nil, 0))
	}
	started := time.Now()
	if err := finish(edge.MeasurementPhaseMeasuring, "", "", nil, 0); err != nil {
		return fail(err)
	}
	base, err := measurementRequestBase(beo.Spec.Session.Observation)
	if err != nil {
		return fail(err)
	}
	run = &sessionRun{c: c, beo: beo, ref: ref, plan: plan, config: config, base: base, started: started}
	if !plan.powered {
		mode := plan.preparation.RunConfig.State.PowerMode
		if _, _, err := run.measure(ctx, "power", servingstate.Operation{Kind: servingstate.OperationPower, PowerMode: mode,
			MaxActuationDuration: plan.maxActuation, Holder: string(beo.UID)}); err != nil {
			return fail(err)
		}
	}
	input := profilederivation.SessionInput{Settings: sessionSettings(beo.Spec.Session, plan), MaxResultBytes: config.Session.MaxResultBytes,
		RuntimeDelivery: plan.preparation.RuntimeDelivery}
	if input.MeterSource, err = json.Marshal(plan.meter.Source); err != nil {
		return fail(err)
	}
	sizes := plan.preparation.BatchSizes
	metadataBatch := 0
	for index, batch := range sizes {
		outcome, err := run.batch(ctx, batch, index == len(sizes)-1)
		if err != nil {
			return fail(err)
		}
		switch {
		case outcome.metadata == nil:
		case input.RuntimeMetadata == nil:
			input.RuntimeMetadata, metadataBatch = outcome.metadata, batch
		default:
			if err := sameRuntimeFacts(*input.RuntimeMetadata, metadataBatch, *outcome.metadata, batch); err != nil {
				return fail(err)
			}
		}
		if outcome.postLoad[1].After(outcome.postLoad[0]) {
			input.PostLoadStart, input.PostLoadEnd = outcome.postLoad[0], outcome.postLoad[1]
		}
		if outcome.stop != nil {
			input.Stop = outcome.stop
			break
		}
		input.Batches = append(input.Batches, outcome.input)
	}
	if len(input.Batches) == 0 {
		// A limit at the first batch size leaves no usable batch: no profile.
		return finish(edge.MeasurementPhaseFailed, string(input.Stop.Class), input.Stop.Reason, nil, 0)
	}
	if input.PostLoadEnd.IsZero() {
		// The stopped batch size's instance did not stay loaded; observe the
		// post-load window on a fresh instance of the last measured one.
		if input.PostLoadStart, input.PostLoadEnd, err = run.postLoadInstance(ctx, input.Batches[len(input.Batches)-1].Batch); err != nil {
			return fail(err)
		}
	}
	if input.Power, err = c.sessionReads.power(ctx, plan.meter, started, input.PostLoadEnd); err != nil {
		return fail(err)
	}
	input.Condition = measuredCondition(plan.preparation, ref)
	if err := ctx.Err(); err != nil {
		return err
	}
	// Derivation owns no controller lock. Its computation may finish after
	// shutdown, but only this run goroutine can publish its result.
	type derivedSession struct {
		result profilederivation.SessionResult
		raw    []byte
		err    error
	}
	derived := make(chan derivedSession, 1)
	go func() {
		result, err := profilederivation.DeriveSession(input)
		var raw []byte
		if err == nil {
			raw, err = json.Marshal(result)
		}
		derived <- derivedSession{result: result, raw: raw, err: err}
	}()
	var complete derivedSession
	select {
	case <-ctx.Done():
		return ctx.Err()
	case complete = <-derived:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if complete.err != nil {
		return fail(complete.err)
	}
	result, raw := complete.result, complete.raw
	identity := evidencestore.ObjectIdentity{Store: config.EvidenceStore, Key: SessionResultPrefix + string(beo.UID) + "/result.json", Size: int64(len(raw))}
	if config.Evidence == nil {
		return fail(errors.New("measurement evidence store is not configured"))
	}
	if _, err := config.Evidence.PutImmutable(ctx, identity, bytes.NewReader(raw)); err != nil {
		return fail(err)
	}
	reference := &edge.ProfilingRunEvidenceReference{Store: identity.Store, Key: identity.Key, SchemaVersion: profilederivation.SessionResultSchemaVersion, Size: identity.Size}
	outcome, message := sessionOutcomeMeasured, "measured batch sizes "+batchList(result.Batches)
	if result.Stop != nil {
		outcome, message = string(result.Stop.Class), result.Stop.Reason
	}
	return finish(edge.MeasurementPhaseComplete, outcome, message, reference, len(result.Batches))
}

// sessionBatchOutcome is one batch size's measurement, or the stop it hit.
type sessionBatchOutcome struct {
	input    profilederivation.SessionBatchInput
	stop     *profilederivation.SessionStop
	metadata *native.RuntimeMetadata
	// postLoad is the window observed after this batch size's load.
	postLoad [2]time.Time
}

// batch prepares a fixed-batch instance, runs its load and attempts removal on
// every path.
func (r *sessionRun) batch(ctx context.Context, batch int, last bool) (outcome sessionBatchOutcome, err error) {
	spec := sessionInstance(r.beo, r.plan.preparation, r.ref, batch, r.config.Session)
	if err := SessionQueueCapacity(batch, spec.Config.MaxQueueRequests, int64(spec.Config.MaxQueueBytes), r.beo.Spec.Session.FramedDecodedRequestUpperBytes); err != nil {
		outcome.stop = &profilederivation.SessionStop{Batch: batch, Class: profilederivation.StopCapacityLimit, Reason: err.Error()}
		return outcome, nil
	}
	prepareStart := time.Now()
	prepared, prepareKey, err := r.measure(ctx, "prepare-b"+strconv.Itoa(batch), servingstate.Operation{Kind: servingstate.OperationPrepare, Prepare: &spec})
	removeOnReturn := true
	defer func() {
		if removeOnReturn {
			_, removeErr := r.removePrepared(ctx, "remove-b"+strconv.Itoa(batch), spec, prepared)
			err = errors.Join(err, removeErr)
		}
	}()
	if err != nil {
		if failure := prepared.Result.Preparation; failure != nil && failure.Failure != nil && strings.Contains(failure.Failure.ReasonKey, ":OOMKilled:") {
			outcome.stop = &profilederivation.SessionStop{Batch: batch, Class: profilederivation.StopCapacityLimit, Reason: firstLine(err.Error())}
			return outcome, nil
		}
		return outcome, err
	}
	instance := schedulerPhysicalState(spec.Instance.ID, prepared.Result.Actual.Instances)
	if instance == nil || instance.PodUID == "" {
		return outcome, fmt.Errorf("prepare of batch %d left no instance", batch)
	}
	if state, ok := prepared.Result.Actual.Native[instance.ID]; ok {
		metadata := state.Metadata.Runtime
		outcome.metadata = &metadata
	}
	if outcome.metadata == nil {
		return outcome, fmt.Errorf("batch %d instance reported no runtime metadata", batch)
	}
	if !prepared.Result.Actual.Native[instance.ID].Ready {
		return outcome, fmt.Errorf("prepare of batch %d left no ready instance", batch)
	}
	load := sessionLoad{batch: batch, podUID: instance.PodUID,
		servingNamespace: r.c.observer.config.Namespace, minimumSamples: r.plan.minimumSamples, config: r.config.Session,
		preparation: r.plan.preparation, actor: sessionActorLimits{maxQueueRequests: spec.Config.MaxQueueRequests, maxQueueBytes: spec.Config.MaxQueueBytes}}
	table, loadErr := r.c.runSessionLoad(ctx, sessionLoadJob(r.beo, r.ref, load), r.config.Session.RunnerTimeout)
	var levels []profilederivation.DriverLevelRow
	if loadErr == nil {
		levels, outcome.stop, loadErr = sessionLevels(table, batch)
	}
	// The post-load window follows the session's last load while its instance
	// stays loaded; a capacity limit leaves no healthy instance to observe.
	if loadErr == nil && len(levels) > 0 && (last || outcome.stop != nil && outcome.stop.Class != profilederivation.StopCapacityLimit) {
		outcome.postLoad, loadErr = r.observePostLoad(ctx, levels[len(levels)-1].End)
	}
	removeKey, removeErr := r.removePrepared(ctx, "remove-b"+strconv.Itoa(batch), spec, prepared)
	removeOnReturn = false
	removeEnd := time.Now()
	if err := errors.Join(removeErr, loadErr); err != nil || outcome.stop != nil {
		return outcome, err
	}
	outcome.input = profilederivation.SessionBatchInput{Batch: batch, PrepareCostKey: prepareKey, RemoveCostKey: removeKey}
	var excluded map[string]bool
	for _, level := range levels {
		if level.Excluded {
			if excluded == nil {
				excluded = map[string]bool{}
			}
			excluded[level.ID] = true
		}
	}
	rows, err := r.c.sessionReads.native(ctx, string(r.beo.UID)+"/"+strconv.Itoa(batch)+"/", r.started, excluded)
	if err != nil {
		return outcome, err
	}
	for _, level := range levels {
		native := rows[level.ID]
		if !level.Excluded && native.inputs != level.CompletedInputs {
			return outcome, fmt.Errorf("level %s: native inputs %d of %d", level.ID, native.inputs, level.CompletedInputs)
		}
		outcome.input.Levels = append(outcome.input.Levels, profilederivation.SessionLevelInput{Row: level, Native: native.rows})
	}
	peak, err := r.c.sessionReads.memory(ctx, instance.PodUID, prepareStart, removeEnd)
	if err != nil {
		return outcome, err
	}
	outcome.input.PeakMemoryBytes, outcome.input.MemoryKind = int64(peak.Bytes), "current-sample-maximum"
	if peak.Lifetime {
		outcome.input.MemoryKind = "peak"
	}
	return outcome, nil
}

// observePostLoad keeps the loaded instance without requests for the
// post-load window after load stopped at start.
func (r *sessionRun) observePostLoad(ctx context.Context, start time.Time) ([2]time.Time, error) {
	end := start.Add(time.Duration(r.beo.Spec.Session.PostLoadWindowSeconds * float64(time.Second)))
	timer := time.NewTimer(time.Until(end))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return [2]time.Time{}, ctx.Err()
	case <-timer.C:
		return [2]time.Time{start, end}, nil
	}
}

// postLoadInstance prepares a batch size again and observes the post-load
// window on it before removing it.
func (r *sessionRun) postLoadInstance(ctx context.Context, batch int) (start, end time.Time, err error) {
	spec := sessionInstance(r.beo, r.plan.preparation, r.ref, batch, r.config.Session)
	prepared, _, err := r.measure(ctx, "prepare-post-b"+strconv.Itoa(batch), servingstate.Operation{Kind: servingstate.OperationPrepare, Prepare: &spec})
	defer func() {
		_, removeErr := r.removePrepared(ctx, "remove-post-b"+strconv.Itoa(batch), spec, prepared)
		err = errors.Join(err, removeErr)
	}()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	instance := schedulerPhysicalState(spec.Instance.ID, prepared.Result.Actual.Instances)
	if instance == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("post-load prepare of batch %d left no instance", batch)
	}
	if instance.PodUID == "" || !prepared.Result.Actual.Native[instance.ID].Ready {
		return time.Time{}, time.Time{}, fmt.Errorf("post-load prepare of batch %d left no ready instance", batch)
	}
	window, observeErr := r.observePostLoad(ctx, time.Now())
	return window[0], window[1], observeErr
}

// sessionLevels maps the level table onto derivation rows and the batch
// size's stop. A level the driver did not measure for its predicted
// duration stays in the batch unless it is saturation, which every other
// level and the batch's service records need.
func sessionLevels(table sessionLevelTable, batch int) ([]profilederivation.DriverLevelRow, *profilederivation.SessionStop, error) {
	if table.Error != "" {
		return nil, nil, errors.New(table.Error)
	}
	if len(table.Levels) == 0 {
		return nil, nil, errors.New("session-load reported no level")
	}
	stop := func(class profilederivation.StopClass, level sessionLevelWire) *profilederivation.SessionStop {
		return &profilederivation.SessionStop{Batch: batch, Class: class, Reason: levelSentence(level, batch)}
	}
	rows := make([]profilederivation.DriverLevelRow, 0, len(table.Levels))
	for _, level := range table.Levels {
		switch level.EndReason {
		case sessionSamplesReached:
		case sessionPredictedOverLimit, sessionRequestLifetimeExpired:
			if level.Kind == profilederivation.LevelSaturation {
				return rows, stop(profilederivation.StopNotMeasured, level), nil
			}
		case sessionServiceOverLifetime:
			return rows, stop(profilederivation.StopNotServable, level), nil
		case sessionAllocationBeforeBatch:
			return rows, stop(profilederivation.StopCapacityLimit, level), nil
		default:
			return nil, nil, errors.New(levelSentence(level, batch))
		}
		rows = append(rows, level.row())
	}
	return rows, nil, nil
}

// levelSentence is the driver's own sentence for a level end, or the end
// reason with the level when the driver gave none.
func levelSentence(level sessionLevelWire, batch int) string {
	switch {
	case level.Detail != "":
		return level.Detail
	case level.EndReason == sessionPredictedOverLimit:
		return fmt.Sprintf("%s level of batch %d predicted %g s, above the %g s level limit", level.Kind, batch, level.PredictedSeconds, level.LimitSeconds)
	}
	return fmt.Sprintf("%s level of batch %d ended %s", level.Kind, batch, level.EndReason)
}

// sessionInstance is the profile-less fixed-batch instance of one batch size,
// prepared like a serving instance from the session's frozen configuration.
func sessionInstance(beo *edge.BatchEnergyOptimization, preparation SessionPreparation, ref serving.NodeReference, batch int, config SessionConfig) servingstate.PrepareSpec {
	sum := sha256.Sum256([]byte(beo.UID))
	name := "session-" + hex.EncodeToString(sum[:8]) + "-b" + strconv.Itoa(batch)
	frozen := preparation.RunConfig.Actor
	actor := preparation.RunConfig.ActorConfig()
	actor.MemoryRequestBytes, actor.MemoryLimitBytes, actor.MaxQueueBytes = frozen.MemoryRequestBytes, frozen.MemoryLimitBytes, frozen.MaxQueueBytes
	actor.Mode = servingstate.NativeProfilingMode
	// The instance initializes, so warms, only its own batch size.
	actor.BatchSizes, actor.MaxBatchSize = []int{batch}, batch
	actor.RunnerTimeoutMS = int(config.RunnerTimeout / time.Millisecond)
	actor.MaxRequestBytes = int(min(config.MaxRequestBytes, int64(native.MaxFrameBytes)))
	actor.Policy = native.Policy{DecisionID: name, DeadlinePolicy: actor.Policy.DeadlinePolicy, FixedBatchSize: batch,
		ProfileID: types.NamespacedName{Namespace: beo.Namespace, Name: beo.Name}.String() + "/" + string(beo.UID)}
	actor.BuildRunnerCommand()
	spec := servingstate.PrepareSpec{Name: name, MemoryBytes: preparation.MemoryBytes, Config: actor,
		Instance: serving.SchedulerInstance{ID: name, Node: ref, Allocation: serving.SchedulerAllocation{GPUUUID: actor.GPUUUID, MemoryBytes: preparation.MemoryBytes},
			Capacity: actor.MaxQueueRequests, Weight: 1, MaxBatch: batch, FixedBatchSize: batch}}
	if actor.Backend == "rknnlite" {
		spec.Delivery.RKNNLite = preparation.RuntimeDelivery
	} else if access := preparation.RunConfig.Delivery.RuntimeAccess; access != nil {
		spec.Delivery.SupplementalGroups = slices.Clone(access.SupplementalGroups)
	}
	return spec
}

func sessionSettings(s *edge.BatchEnergyOptimizationSessionSpec, plan sessionPlan) profilederivation.SessionSettings {
	return profilederivation.SessionSettings{MeterLagSeconds: s.MeterLagSeconds, MinimumPowerWindowSeconds: s.MinimumPowerWindowSeconds,
		MidLevelRatio: s.MidLevelRatio, SparseMeanGapSeconds: s.SparseMeanGapSeconds, LevelDurationLimitSeconds: s.LevelDurationLimitSeconds,
		PostLoadWindowSeconds: s.PostLoadWindowSeconds, MinimumSamples: plan.minimumSamples, ConfidenceLevel: plan.confidence}
}

// measuredCondition is the configuration the session measured, from its
// frozen RunConfig.
func measuredCondition(preparation SessionPreparation, ref serving.NodeReference) profilederivation.SessionCondition {
	config := preparation.RunConfig
	state := config.State
	condition := profilederivation.SessionCondition{NodeName: ref.Name, NodeUID: ref.UID, DeviceClass: state.DeviceClass, PowerMode: state.PowerMode,
		LogicalModel: state.Model, RuntimeFamily: state.RuntimeFamily, Backend: state.Backend, RuntimeImage: config.Images.Runtime,
		Artifact: profilederivation.CurveArtifactContract{ExecutionPath: config.Candidate.ExecutionPath, Name: state.Artifact,
			Files: slices.Clone(state.ArtifactFiles), Image: config.Images.Artifact},
		Preprocessor: profilederivation.CurvePreprocessorContract{Name: state.Preprocessor.Name, Image: state.Preprocessor.Image,
			Architecture: state.Preprocessor.Architecture, Path: state.Preprocessor.Path, MaxNewTokens: state.Preprocessor.MaxNewTokens,
			Inputs: slices.Clone(state.Preprocessor.Inputs), Outputs: slices.Clone(state.Preprocessor.Outputs)},
		Dataset:    &profilederivation.CurveDatasetContract{Name: preparation.Dataset.Name, UID: preparation.Dataset.UID},
		Firstfloor: state.Firstfloor, Actor: config.Actor}
	if access := config.Delivery.RuntimeAccess; access != nil {
		condition.RuntimeAccess = &profilederivation.CurveRuntimeAccessContract{Privileged: access.Privileged,
			SupplementalGroups: slices.Clone(access.SupplementalGroups), Resources: access.Resources, GPUUUID: access.GPUUUID}
	}
	return condition
}

// sameRuntimeFacts compares the runtime facts profile consumers read (the
// catalog's backend and condition, the serving instance's measured runtime
// check) between two instances of one session; the result keeps one report.
func sameRuntimeFacts(kept native.RuntimeMetadata, keptBatch int, next native.RuntimeMetadata, batch int) error {
	facts := func(m native.RuntimeMetadata) []string {
		return []string{m.Runtime, m.RuntimeVersion, fmt.Sprint(m.Providers), m.Device, m.GPUUUID, fmt.Sprint(m.ModelFiles)}
	}
	names := []string{"runtime", "runtime version", "providers", "device", "GPU UUID", "model files"}
	want, got := facts(kept), facts(next)
	for i, name := range names {
		if want[i] != got[i] {
			return fmt.Errorf("the batch %d instance reports %s %s, the batch %d instance %s", batch, name, got[i], keptBatch, want[i])
		}
	}
	return nil
}

func batchList(batches []profilederivation.SessionBatch) string {
	sizes := make([]string, len(batches))
	for i, batch := range batches {
		sizes[i] = strconv.Itoa(batch.Batch)
	}
	return strings.Join(sizes, ", ")
}

// SessionQueueCapacity checks whether saturation's two outstanding batches
// fit the native request and byte limits.
func SessionQueueCapacity(batch, maxQueueRequests int, maxQueueBytes, requestUpperBytes int64) error {
	if int64(batch) > min(int64(maxQueueRequests), maxQueueBytes/requestUpperBytes)/2 {
		return fmt.Errorf("saturation keeps %d requests (two batches of %d) outstanding, above the native "+
			"queue limit of %d requests or %d bytes at %d bytes per request",
			2*int64(batch), batch, maxQueueRequests, maxQueueBytes, requestUpperBytes)
	}
	return nil
}
