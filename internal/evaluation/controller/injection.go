package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

var errInjectionUpdated = errors.New("injection status persisted")
var errInjectionSelection = errors.New("injection selection is invalid")
var errInjectionExpired = errors.New("injection window expired before mutation")

type servingInstance struct {
	ID               string                        `json:"id"`
	WorkloadUID      string                        `json:"workloadUID"`
	OwnerWorkloadUID string                        `json:"ownerWorkloadUID"`
	Ready            bool                          `json:"ready"`
	Stopped          bool                          `json:"stopped"`
	PodName          string                        `json:"podName"`
	PodUID           types.UID                     `json:"podUID"`
	Node             api.EvaluationObjectReference `json:"node"`
}
type schedulerState struct {
	Snapshot *struct {
		At    time.Time `json:"at"`
		Nodes []struct {
			Node  api.EvaluationObjectReference `json:"node"`
			Ready bool                          `json:"ready"`
		} `json:"nodes"`
		Instances    []servingInstance             `json:"instances"`
		Routing      map[string]map[string]float64 `json:"routing"`
		Reservations []any                         `json:"reservations"`
		Outstanding  []any                         `json:"outstanding"`
		Actions      []struct {
			Status string `json:"status"`
		} `json:"actions"`
	} `json:"snapshot"`
	Results []struct {
		EndedAt time.Time `json:"endedAt"`
	} `json:"results"`
}

func seconds(v map[string]string, key string) time.Duration {
	n, _ := strconv.ParseFloat(v[key], 64)
	return time.Duration(n * float64(time.Second))
}
func (s schedulerState) fresh(now time.Time, v map[string]string) bool {
	return s.Snapshot != nil && !s.Snapshot.At.IsZero() && !s.Snapshot.At.After(now) && now.Sub(s.Snapshot.At) <= seconds(v, "injectionSnapshotTTLSeconds")
}
func (s schedulerState) returned(now time.Time, v map[string]string) bool {
	if !s.fresh(now, v) || len(s.Snapshot.Reservations) > 0 || len(s.Snapshot.Outstanding) > 0 {
		return false
	}
	for _, instance := range s.Snapshot.Instances {
		if instance.OwnerWorkloadUID != "" {
			return false
		}
	}
	for _, routes := range s.Snapshot.Routing {
		for _, weight := range routes {
			if weight > 0 {
				return false
			}
		}
	}
	for _, a := range s.Snapshot.Actions {
		if a.Status == "running" || a.Status == "cancel" {
			return false
		}
	}
	for _, result := range s.Results {
		if !s.Snapshot.At.After(result.EndedAt) {
			return false
		}
	}
	return true
}

type loadWindow struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Workloads []struct {
		UID                 string             `json:"uid"`
		InstanceAssignments map[string]float64 `json:"instanceAssignments"`
	} `json:"workloads"`
}
type injectionEvidence struct {
	RunUID     types.UID                `json:"runUID"`
	StageUID   types.UID                `json:"stageUID"`
	StageID    string                   `json:"stageID"`
	Record     api.FaultInjectionRecord `json:"record"`
	State      *schedulerState          `json:"-"`
	Load       *loadWindow              `json:"-"`
	StateRaw   json.RawMessage          `json:"state,omitempty"`
	LoadRaw    json.RawMessage          `json:"load,omitempty"`
	Candidates []servingInstance        `json:"candidates,omitempty"`
	Rejected   map[string]string        `json:"rejected,omitempty"`
	Requests   []map[string]any         `json:"requests,omitempty"`
}

func (r *Runtime) injections(ctx context.Context, parent *api.EvaluationRun, stage *api.EvaluationStage) error {
	// Any unknown response terminates the injection stream, not a retry trigger.
	for _, record := range stage.Status.InjectionRecords {
		if record.Outcome == injectionUnknown {
			return nil
		}
	}
	v := parent.Status.FrozenConfig.Values
	store, err := r.Store(ctx, parent.Namespace, parent.Spec.EvidenceStore, v)
	if err != nil {
		return err
	}
	if stage.Status.GoRef == nil {
		var goEvidence struct {
			RunUID       types.UID                         `json:"runUID"`
			StageUID     types.UID                         `json:"stageUID"`
			StageID      string                            `json:"stageID"`
			GoUTC        metav1.Time                       `json:"goUTC"`
			ProducerRefs []api.EvaluationProducerReference `json:"producerRefs"`
		}
		reference, err := readJSON(ctx, store, parent.Spec.EvidenceStore, evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/go.json"), &goEvidence)
		if errors.Is(err, evidencestore.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if goEvidence.RunUID != parent.UID || goEvidence.StageUID != stage.UID || goEvidence.StageID != stage.Spec.StageID || goEvidence.GoUTC.IsZero() {
			return fmt.Errorf("GO identity/time mismatch")
		}
		before := stage.DeepCopy()
		stage.Status.GoRef = reference
		stage.Status.ProducerRefs = goEvidence.ProducerRefs
		if err := r.patchStatus(ctx, stage, before); err != nil {
			return err
		}
		return errInjectionUpdated
	}
	var goEvidence struct {
		GoUTC metav1.Time `json:"goUTC"`
	}
	if _, err = readJSON(ctx, store, stage.Status.GoRef.Store, stage.Status.GoRef.Key, &goEvidence); err != nil {
		return err
	}
	for _, plan := range stage.Spec.FailureInjection {
		var record *api.FaultInjectionRecord
		for i := range stage.Status.InjectionRecords {
			if stage.Status.InjectionRecords[i].ID == plan.ID {
				record = &stage.Status.InjectionRecords[i]
			}
		}
		if record != nil && record.Outcome != injectionRequested {
			continue
		}
		scheduled := goEvidence.GoUTC.Add(time.Duration(plan.OffsetSeconds * float64(time.Second)))
		now := r.now()
		if now.Before(&metav1.Time{Time: scheduled}) {
			continue
		}
		key := evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/injections/"+plan.ID+".json")
		// An upload/status response loss is adopted from independently read evidence.
		var saved injectionEvidence
		savedRef, readErr := readJSON(ctx, store, parent.Spec.EvidenceStore, key, &saved)
		if readErr == nil {
			if saved.RunUID != parent.UID || saved.StageUID != stage.UID || saved.Record.ID != plan.ID {
				return fmt.Errorf("injection evidence identity mismatch")
			}
			saved.Record.EvidenceRef = savedRef
			if record == nil {
				stage.Status.InjectionRecords = append(stage.Status.InjectionRecords, saved.Record)
			} else {
				*record = saved.Record
			}
			r.injectionVerdict(stage, saved.Record)
			return nil
		}
		if !errors.Is(readErr, evidencestore.ErrNotFound) {
			return readErr
		}
		return r.executeInjection(ctx, parent, stage, plan, record, scheduled, store, key)
	}
	return nil
}

func (r *Runtime) executeInjection(ctx context.Context, parent *api.EvaluationRun, stage *api.EvaluationStage, plan api.FaultInjectionPlan, record *api.FaultInjectionRecord, scheduled time.Time, store ObjectStore, key string) error {
	v := parent.Status.FrozenConfig.Values
	now := r.now()
	var err error
	evidence := injectionEvidence{RunUID: parent.UID, StageUID: stage.UID, StageID: stage.Spec.StageID, Record: api.FaultInjectionRecord{ID: plan.ID, ScheduledAt: metav1.NewTime(scheduled)}}
	if record != nil {
		evidence.Record = *record
		evidence.Record.Outcome = injectionUnknown
		evidence.Record.Reason = api.EvaluationReasonRemoteEffectUnknown
	} else if now.Sub(scheduled) > seconds(v, "injectionWindowSeconds") {
		evidence.Record.Outcome = injectionInvalid
		evidence.Record.Reason = api.EvaluationReasonInjectionInvalid
	} else {
		if _, err = r.activeParent(ctx, stage); err != nil {
			return err
		}
		if err = r.selectInjection(ctx, parent, stage, plan, &evidence); err != nil {
			if !errors.Is(err, errInjectionSelection) {
				return err
			}
			evidence.Record.Outcome = injectionInvalid
			evidence.Record.Reason = api.EvaluationReasonInjectionInvalid
			evidence.Requests = append(evidence.Requests, map[string]any{"selectionError": err.Error()})
		} else {
			err = r.applyInjection(ctx, parent, stage, plan, &evidence)
			if err != nil {
				if len(evidence.Requests) == 0 {
					if !errors.Is(err, errInjectionExpired) && !errors.Is(err, errInjectionSelection) {
						return err
					}
					evidence.Record.Outcome = injectionInvalid
					evidence.Record.Reason = api.EvaluationReasonInjectionInvalid
				} else {
					evidence.Record.Outcome = injectionUnknown
					evidence.Record.Reason = api.EvaluationReasonRemoteEffectUnknown
				}
				evidence.Requests = append(evidence.Requests, map[string]any{"error": err.Error()})
			} else {
				evidence.Record.Outcome = complete
				if plan.Action == actionUncordon {
					at := r.now()
					evidence.Record.EffectObservedAt = &at
				}
			}
		}
	}
	evidence.Record.EvidenceRef = nil
	reference, err := writeJSON(ctx, store, parent.Spec.EvidenceStore, key, evidence)
	if err != nil {
		return err
	}
	before := stage.DeepCopy()
	evidence.Record.EvidenceRef = reference
	found := false
	for i := range stage.Status.InjectionRecords {
		if stage.Status.InjectionRecords[i].ID == plan.ID {
			stage.Status.InjectionRecords[i] = evidence.Record
			found = true
		}
	}
	if !found {
		stage.Status.InjectionRecords = append(stage.Status.InjectionRecords, evidence.Record)
	}
	r.injectionVerdict(stage, evidence.Record)
	if err = r.patchStatus(ctx, stage, before); err != nil {
		return err
	}
	return errInjectionUpdated
}
func (r *Runtime) injectionVerdict(stage *api.EvaluationStage, record api.FaultInjectionRecord) {
	if record.Outcome == injectionInvalid {
		stage.Status.MeasurementVerdict = api.MeasurementINVALID
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionMeasurementPassed, metav1.ConditionFalse, api.EvaluationReasonInjectionInvalid, record.ID)
	}
	if record.Outcome == injectionUnknown {
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, api.EvaluationReasonRemoteEffectUnknown, record.ID)
		stage.Status.ResidualStateRef = record.EvidenceRef
	}
}
func (r *Runtime) selectInjection(ctx context.Context, parent *api.EvaluationRun, stage *api.EvaluationStage, plan api.FaultInjectionPlan, e *injectionEvidence) error {
	if plan.Action == actionUncordon {
		for _, prior := range stage.Status.InjectionRecords {
			if prior.ID == plan.PriorInjectionID && prior.Outcome == complete && prior.NodeRef != nil {
				e.Record.NodeRef = prior.NodeRef
				at := r.now()
				e.Record.SelectedAt = &at
				return nil
			}
		}
		return fmt.Errorf("%w: prior cordon node unavailable", errInjectionSelection)
	}
	v := parent.Status.FrozenConfig.Values
	state := &schedulerState{}
	raw, err := r.observeJSON(ctx, parent, "/api/v1/scheduler/state", state)
	e.State, e.StateRaw = state, raw
	if err != nil {
		return err
	}
	if !state.fresh(r.now().Time, v) {
		return fmt.Errorf("%w: scheduler snapshot expired", errInjectionSelection)
	}
	var settings struct {
		ObservationWindowNanos int64 `json:"observationWindowNanos"`
	}
	if err := r.HTTP(ctx, parent.Namespace, v, "/api/v1/scheduler/settings", &settings); err != nil {
		return err
	}
	if settings.ObservationWindowNanos <= 0 {
		return fmt.Errorf("%w: observation window missing", errInjectionSelection)
	}
	from := state.Snapshot.At.Add(-time.Duration(settings.ObservationWindowNanos))
	query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {state.Snapshot.At.Format(time.RFC3339Nano)}}
	load := &loadWindow{}
	raw, err = r.observeJSON(ctx, parent, "/api/v1/observations/load?"+query.Encode(), load)
	e.Load, e.LoadRaw = load, raw
	if err != nil {
		return err
	}
	e.State, e.Load, e.Rejected = state, load, map[string]string{}
	if !load.From.Equal(from) || !load.To.Equal(state.Snapshot.At) {
		return fmt.Errorf("%w: load window differs from scheduler snapshot", errInjectionSelection)
	}
	allowed := map[string]bool{}
	for _, uid := range plan.Selection.WorkloadUIDs {
		allowed[string(uid)] = true
	}
	excluded := types.UID("")
	if id := plan.Selection.DifferentNodeFrom; id != "" {
		for _, record := range stage.Status.InjectionRecords {
			if record.ID == id && record.NodeRef != nil {
				excluded = record.NodeRef.UID
			}
		}
		if excluded == "" {
			return fmt.Errorf("%w: different-node predecessor missing", errInjectionSelection)
		}
	}
	nodes := map[string]types.UID{}
	for _, node := range state.Snapshot.Nodes {
		if node.Ready {
			nodes[node.Node.Name] = node.Node.UID
		}
	}
	assignments := map[string]map[string]float64{}
	for _, w := range load.Workloads {
		assignments[w.UID] = w.InstanceAssignments
	}
	for _, instance := range state.Snapshot.Instances {
		if !eligible(instance, allowed, nodes, state.Snapshot.Routing, assignments, excluded) {
			e.Rejected[instance.ID] = "not allowed/owned/ready/positive-route/assigned/live-node or excluded"
			continue
		}
		e.Candidates = append(e.Candidates, instance)
	}
	sort.Slice(e.Candidates, func(i, j int) bool {
		a, b := e.Candidates[i], e.Candidates[j]
		for _, key := range plan.Selection.OrderBy {
			var x, y string
			switch key {
			case "node":
				x, y = a.Node.Name, b.Node.Name
			case "pod":
				x, y = a.PodName, b.PodName
			case "instance":
				x, y = a.ID, b.ID
			}
			if x != y {
				return x < y
			}
		}
		return a.ID < b.ID
	})
	if len(e.Candidates) == 0 {
		return fmt.Errorf("%w: no positive-route/assigned serving target", errInjectionSelection)
	}
	target := e.Candidates[0]
	e.Record.PodRef = &api.EvaluationObjectReference{Name: target.PodName, UID: target.PodUID}
	e.Record.NodeRef = &target.Node
	at := r.now()
	e.Record.SelectedAt = &at
	return nil
}
func (r *Runtime) applyInjection(ctx context.Context, parent *api.EvaluationRun, stage *api.EvaluationStage, plan api.FaultInjectionPlan, e *injectionEvidence) error {
	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: e.Record.NodeRef.Name}, node); err != nil {
		return err
	}
	if node.UID != e.Record.NodeRef.UID {
		return fmt.Errorf("%w: Node UID changed", errInjectionSelection)
	}
	if plan.Action != actionUncordon {
		pod := &corev1.Pod{}
		if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: parent.Namespace, Name: e.Record.PodRef.Name}, pod); err != nil {
			return err
		}
		if pod.UID != e.Record.PodRef.UID || pod.Spec.NodeName != node.Name {
			return fmt.Errorf("%w: Pod UID/node changed", errInjectionSelection)
		}
	}
	if plan.Action == actionCordonDelete || plan.Action == actionUncordon {
		before := node.DeepCopy()
		node.Spec.Unschedulable = plan.Action == actionCordonDelete
		if plan.Action == actionCordonDelete && before.Spec.Unschedulable {
			return fmt.Errorf("%w: node was already cordoned; cannot own uncordon", errInjectionSelection)
		}
		if err := r.injectionReady(ctx, parent, stage, e); err != nil {
			return err
		}
		if err := r.markInjectionRequest(ctx, stage, plan, e); err != nil {
			return err
		}
		e.Requests = append(e.Requests, map[string]any{"method": "PATCH", "path": "/api/v1/nodes/" + node.Name, "body": map[string]any{"metadata": map[string]any{"uid": node.UID, "resourceVersion": before.ResourceVersion}, "spec": map[string]any{"unschedulable": node.Spec.Unschedulable}}})
		if err := r.Client.Patch(ctx, node, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		response := r.now()
		e.Record.RespondedAt = &response
		e.Requests[len(e.Requests)-1]["response"] = map[string]any{"uid": node.UID, "resourceVersion": node.ResourceVersion, "unschedulable": node.Spec.Unschedulable}
	}
	if plan.Action != actionUncordon {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: parent.Namespace, Name: e.Record.PodRef.Name}}
		uid := e.Record.PodRef.UID
		if err := r.injectionReady(ctx, parent, stage, e); err != nil {
			return err
		}
		if len(e.Requests) == 0 {
			if err := r.markInjectionRequest(ctx, stage, plan, e); err != nil {
				return err
			}
		}
		e.Requests = append(e.Requests, map[string]any{"method": "DELETE", "path": "/api/v1/namespaces/" + parent.Namespace + "/pods/" + pod.Name, "body": metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}})
		if err := r.Client.Delete(ctx, pod, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
			return err
		}
		response := r.now()
		e.Record.RespondedAt = &response
		e.Requests[len(e.Requests)-1]["response"] = "accepted"
		// Accepted deletion is not proof of physical disappearance.
		return nil
	}
	return nil
}

// Read/selection checks precede intent persistence; the next operation is the
// actual mutation. A status conflict or a failed read never claims remote effects.
func (r *Runtime) markInjectionRequest(ctx context.Context, stage *api.EvaluationStage, plan api.FaultInjectionPlan, e *injectionEvidence) error {
	before := stage.DeepCopy()
	at := r.now()
	offset := at.Sub(e.Record.ScheduledAt.Time).Seconds() + plan.OffsetSeconds
	e.Record.Outcome = injectionRequested
	e.Record.RequestedAt = &at
	e.Record.ActualOffsetSeconds = &offset
	stage.Status.InjectionRecords = append(stage.Status.InjectionRecords, e.Record)
	return r.patchStatus(ctx, stage, before)
}

func (r *Runtime) injectionReady(ctx context.Context, parent *api.EvaluationRun, stage *api.EvaluationStage, e *injectionEvidence) error {
	if _, err := r.activeParent(ctx, stage); err != nil {
		return err
	}
	v := parent.Status.FrozenConfig.Values
	if r.now().Sub(e.Record.ScheduledAt.Time) > seconds(v, "injectionWindowSeconds") || e.State != nil && !e.State.fresh(r.now().Time, v) {
		return errInjectionExpired
	}
	return nil
}

func eligible(i servingInstance, allowed map[string]bool, nodes map[string]types.UID, routing, assignments map[string]map[string]float64, excluded types.UID) bool {
	return allowed[i.WorkloadUID] && i.OwnerWorkloadUID == i.WorkloadUID && i.Ready && !i.Stopped &&
		routing[i.WorkloadUID][i.ID] > 0 && assignments[i.WorkloadUID][i.ID] > 0 &&
		i.PodName != "" && i.PodUID != "" && i.Node.UID != "" && nodes[i.Node.Name] == i.Node.UID && i.Node.UID != excluded
}

func (r *Runtime) finishInjections(ctx context.Context, parent *api.EvaluationRun, stage *api.EvaluationStage, result *stepResult) error {
	if len(stage.Spec.FailureInjection) == len(stage.Status.InjectionRecords) {
		return nil
	}
	if stage.Status.GoRef == nil {
		stage.Status.MeasurementVerdict = api.MeasurementINVALID
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionMeasurementPassed, metav1.ConditionFalse, api.EvaluationReasonInjectionInvalid, "stage ended without GO evidence")
		condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, api.EvaluationReasonEvidenceUnreadable, "missing GO evidence")
		evidence, err := r.audit(ctx, parent, stage.UID, scope(stage.Spec.StageID)+"/missing-go.json", "", result)
		stage.Status.ResidualStateRef = evidence
		return err
	}
	store, err := r.Store(ctx, parent.Namespace, parent.Spec.EvidenceStore, parent.Status.FrozenConfig.Values)
	if err != nil {
		return err
	}
	var goEvidence struct {
		GoUTC metav1.Time `json:"goUTC"`
	}
	if _, err = readJSON(ctx, store, stage.Status.GoRef.Store, stage.Status.GoRef.Key, &goEvidence); err != nil {
		return err
	}
	for _, plan := range stage.Spec.FailureInjection {
		exists := false
		for _, record := range stage.Status.InjectionRecords {
			if record.ID == plan.ID {
				exists = true
			}
		}
		if exists {
			continue
		}
		key := evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/injections/"+plan.ID+".json")
		saved := injectionEvidence{}
		reference, err := readJSON(ctx, store, parent.Spec.EvidenceStore, key, &saved)
		if errors.Is(err, evidencestore.ErrNotFound) {
			saved = injectionEvidence{RunUID: parent.UID, StageUID: stage.UID, StageID: stage.Spec.StageID, Record: api.FaultInjectionRecord{ID: plan.ID, ScheduledAt: metav1.NewTime(goEvidence.GoUTC.Add(time.Duration(plan.OffsetSeconds * float64(time.Second)))), Outcome: injectionInvalid, Reason: api.EvaluationReasonInjectionInvalid}}
			reference, err = writeJSON(ctx, store, parent.Spec.EvidenceStore, key, saved)
		}
		if err != nil {
			return err
		}
		if saved.RunUID != parent.UID || saved.StageUID != stage.UID || saved.Record.ID != plan.ID {
			return fmt.Errorf("injection evidence identity mismatch")
		}
		saved.Record.EvidenceRef = reference
		stage.Status.InjectionRecords = append(stage.Status.InjectionRecords, saved.Record)
		r.injectionVerdict(stage, saved.Record)
	}
	return nil
}

func (r *Runtime) observeJSON(ctx context.Context, parent *api.EvaluationRun, path string, value any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := r.HTTP(ctx, parent.Namespace, parent.Status.FrozenConfig.Values, path, &raw); err != nil {
		return nil, err
	}
	return raw, json.Unmarshal(raw, value)
}
