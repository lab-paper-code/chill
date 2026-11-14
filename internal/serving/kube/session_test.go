package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	servingnative "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	batchv1 "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const sessionSampleSpacing = 250 * time.Millisecond

// sessionHarness runs measurement sessions against fake operations, a fake
// load Job outcome per batch size and fake observation records laid out on a
// past timeline. tables sets a batch size's level table; measured lists the
// measured operations by name.
type sessionHarness struct {
	*admissionHarness
	evidence *memEvidence
	origin   time.Time

	mu       sync.Mutex
	measured []string
	jobs     []*batchv1.Job
	tables   map[int]sessionLevelTable
	native   map[string][]profilederivation.NativeBatchRow
	spans    [][2]time.Time
	// prepareErr fails a batch size's prepare with the given failure.
	prepareErr map[int]*serving.PreparationFailureObservation
	// dropRow removes one native row of this workload identifier.
	dropRow string
	nextID  uint64
	// runtime replaces a batch size's captured native runtime metadata; a
	// nil entry captures no native state.
	runtime map[int]*native.RuntimeMetadata
}

func sessionSettingsFixture() *edge.BatchEnergyOptimizationSessionSpec {
	return &edge.BatchEnergyOptimizationSessionSpec{
		MeterLagSeconds: 0.5, MinimumPowerWindowSeconds: 1, MidLevelRatio: .25, SparseMeanGapSeconds: 1,
		LevelDurationLimitSeconds: 600, PostLoadWindowSeconds: 2, ConfidenceLevel: "0.95",
		RequestLifetimeSeconds: 30, DeliveryMarginMilliseconds: 100, FramedDecodedRequestUpperBytes: 1024,
		Observation: edge.MeasurementObservationSpec{IntervalMilliseconds: 1000, RequestTimeoutMilliseconds: 800, MaximumOperationSeconds: 900, MaximumGapMilliseconds: 3000,
			Timing: &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "1", MaximumRequestSeconds: "0.2", MaximumReceiptGapSeconds: "0.3", Source: "fixture"}},
	}
}

func sessionPreparationFixture() SessionPreparation {
	config := profilingrun.RunConfig{
		Candidate: profilingrun.CandidateIdentity{ExecutionPath: "ort-cpu"},
		State: profilingrun.ExecutionState{DeviceClass: "cpu", Model: "mobilenet", Artifact: "mobilenet-onnx", ArtifactFiles: []string{"model.onnx"},
			RuntimeFamily: "onnxruntime", Backend: "ort-cpu", PowerMode: maxMode,
			Firstfloor:   &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 64},
			Preprocessor: profilingrun.PreprocessorState{Name: "imagenet", Image: "pre:1", Architecture: "amd64", Path: "/pre", Inputs: []string{"input"}, Outputs: []string{"output"}}},
		Images:      profilingrun.Images{Runtime: "runtime:1", Artifact: "artifact:1"},
		Measurement: profilingrun.MeasurementContract{WorkloadResourceVersion: "42"},
		Actor:       native.Config{MemoryRequestBytes: 256 << 20, MemoryLimitBytes: 1 << 30, MaxQueueBytes: 1 << 20},
	}
	return SessionPreparation{RunConfig: config, MemoryBytes: 1 << 30, BatchSizes: []int{1, 2, 4}, SLOPercentile: 95, SLOTarget: 200 * time.Millisecond,
		Dataset: profilingrun.DatasetContract{Name: "imagenet-val", UID: "dataset-uid", Image: "dataset:1", Path: "/datasets/imagenet"}}
}

func sessionRoot(session *edge.BatchEnergyOptimizationSessionSpec) *edge.BatchEnergyOptimization {
	beo := beoRoot("session", testNode.Name, "")
	beo.Spec.Session = session
	return beo
}

// beoStatusClient serves the BatchEnergyOptimization status subresource the
// shared fake store does not register: a status patch patches the object.
type beoStatusClient struct{ client.Client }

func (c beoStatusClient) Status() client.SubResourceWriter {
	return beoStatusWriter{SubResourceWriter: c.Client.Status(), whole: c.Client}
}

type beoStatusWriter struct {
	client.SubResourceWriter
	whole client.Client
}

func (w beoStatusWriter) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if _, ok := object.(*edge.BatchEnergyOptimization); ok {
		return w.whole.Patch(ctx, object, patch)
	}
	return w.SubResourceWriter.Patch(ctx, object, patch, opts...)
}

func newSessionHarness(t *testing.T) *sessionHarness {
	t.Helper()
	h, _, evidence := sequenceHarness(t)
	h.controller.store.client = beoStatusClient{Client: h.controller.store.client}
	s := &sessionHarness{admissionHarness: h, evidence: evidence, origin: time.Now().Add(-10 * time.Minute).Truncate(time.Second),
		tables: map[int]sessionLevelTable{}, native: map[string][]profilederivation.NativeBatchRow{}, prepareErr: map[int]*serving.PreparationFailureObservation{}}
	class := cpuClass()
	h.create(class)
	h.controller.mu.Lock()
	h.controller.config.Meters = []servingops.MeterDeclaration{{ID: "wall", Node: testNode, Scope: servingstate.MeasurementWallScope,
		Source: metricobserver.SourceIdentity{Kind: "fixture", NodeName: testNode.Name, Endpoint: "http://meter", Metric: "watts"}}}
	for _, m := range class.Spec.PowerModes {
		h.controller.config.PowerPolicies = append(h.controller.config.PowerPolicies, nodeactuator.ActuationPolicySpec{Name: m.Name, DeviceClass: class.Name,
			PowerMode: m.Name, ActuatorKind: string(m.Actuator.Kind), SettlingDurationSeconds: 1, MaxActuationDurationSeconds: 60})
	}
	h.controller.config.Session = SessionConfig{Image: "chill-server:1", Namespace: "chill-system", ServiceAccount: "session-load", NodeName: "chill-server-node",
		MaxResultBytes: 64 << 20, RunnerTimeout: time.Minute, MaxRequestBytes: 4 << 20,
		Prepare: func(context.Context, *edge.BatchEnergyOptimization) (SessionPreparation, error) {
			return sessionPreparationFixture(), nil
		}}
	h.controller.mu.Unlock()
	cursor := s.origin
	for _, batch := range []int{1, 2, 4} {
		s.tables[batch] = s.layOut(batch, &cursor)
	}
	h.controller.measureStep = s.measure
	h.controller.sessionReads = sessionReads{
		native: func(_ context.Context, prefix string, _ time.Time) (map[string][]profilederivation.NativeBatchRow, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			// The fake API assigns the BEO UID; tables use a fixed fixture UID.
			prefix = "session-uid" + prefix[strings.IndexByte(prefix, '/'):]
			rows := map[string][]profilederivation.NativeBatchRow{}
			for uid, nativeRows := range s.native {
				if strings.HasPrefix(uid, prefix) {
					rows[uid] = slices.Clone(nativeRows)
					if uid == s.dropRow {
						rows[uid] = rows[uid][1:]
					}
				}
			}
			if len(rows) == 0 {
				return nil, fmt.Errorf("fixture has no native rows for prefix %q", prefix)
			}
			return rows, nil
		},
		power: func(_ context.Context, _ servingops.MeterDeclaration, _, to time.Time) ([]profilederivation.Sample, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			var samples []profilederivation.Sample
			for at := s.origin.Add(-time.Minute); !at.After(to); at = at.Add(sessionSampleSpacing) {
				watts := 60.0
				for _, span := range s.spans {
					if !at.Before(span[0]) && !at.After(span[1]) {
						watts = 100
					}
				}
				samples = append(samples, profilederivation.Sample{ObservedAt: at, Watts: watts})
			}
			return samples, nil
		},
		memory: func(context.Context, string, time.Time, time.Time) (observation.PodMemoryPeak, error) {
			return observation.PodMemoryPeak{Bytes: 700 << 20, Lifetime: true}, nil
		},
	}
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	h.kube.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		args := job.Spec.Template.Spec.Containers[0].Args
		batch, _ := strconv.Atoi(args[slices.Index(args, "--batch")+1])
		s.mu.Lock()
		s.jobs = append(s.jobs, job.DeepCopy())
		message, _ := json.Marshal(s.tables[batch])
		s.mu.Unlock()
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: core.ConditionTrue}}
		pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: job.Namespace, Name: job.Name + "-pod", Labels: map[string]string{"job-name": job.Name}},
			Status: core.PodStatus{Phase: core.PodSucceeded, ContainerStatuses: []core.ContainerStatus{{Name: sessionLoadContainer,
				State: core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: "Completed", Message: string(message)}}}}}}
		return false, nil, h.kube.Tracker().Create(pods, pod, pod.Namespace)
	})
	return s
}

// layOut places one batch size's levels on the timeline after cursor: the
// executor rows of each level and the level row the driver reports.
func (s *sessionHarness) layOut(batch int, cursor *time.Time) sessionLevelTable {
	type level struct {
		kind            string
		period, service time.Duration
		count           int
	}
	levels := []level{{profilederivation.LevelSaturation, 20 * time.Millisecond, 20 * time.Millisecond, 150}, {profilederivation.LevelMid, 80 * time.Millisecond, 20 * time.Millisecond, 60}}
	if batch == 1 {
		levels = append(levels, level{profilederivation.LevelSparse, 600 * time.Millisecond, 20 * time.Millisecond, 6})
	}
	var table sessionLevelTable
	finished := time.Time{}
	for _, l := range levels {
		id := fmt.Sprintf("session-uid/%d/%s", batch, l.kind)
		start := *cursor
		at := start
		for range l.count {
			s.nextID++
			row := profilederivation.NativeBatchRow{BatchID: s.nextID, BatchSize: batch,
				StartedAt: at, FinishedAt: at.Add(l.service), ServiceDurationNanos: l.service.Nanoseconds(), LatestEnqueuedAt: at}
			if !finished.IsZero() {
				idle := at.Sub(finished).Nanoseconds()
				row.PrecedingIdleNanos = &idle
			}
			finished = row.FinishedAt
			s.native[id] = append(s.native[id], row)
			at = at.Add(l.period)
		}
		end := at
		s.spans = append(s.spans, [2]time.Time{start, end})
		table.Levels = append(table.Levels, sessionLevelWire{Kind: l.kind, WorkloadUID: id, StartedAt: start, EndedAt: end,
			SentInputs: l.count * batch, CompletedInputs: l.count * batch, CompletedBatches: l.count, EndReason: sessionSamplesReached})
		*cursor = end.Add(time.Second)
	}
	*cursor = cursor.Add(5 * time.Second)
	return table
}

func (s *sessionHarness) measure(_ context.Context, _ SchedulerControllerConfig, request servingops.MeasurementRequest) (servingops.OperationMeasurement, evidencestore.Receipt, error) {
	operation := request.Operation
	name := operation.ID[strings.LastIndex(operation.ID, ".")+1:]
	s.mu.Lock()
	s.measured = append(s.measured, name)
	s.mu.Unlock()
	record := servingops.OperationMeasurement{Request: request, Verdict: servingstate.MeasurementAccepted}
	receipt := evidencestore.Receipt{Object: evidencestore.ObjectIdentity{Key: servingops.OperationMeasurementKey(operation)}}
	if prepare := operation.Prepare; prepare != nil {
		batch := prepare.Config.Policy.FixedBatchSize
		if failure := s.prepareErr[batch]; failure != nil {
			record.Result.Preparation = &servingstate.PreparationEvidence{PodUID: "oom-pod", Failure: failure}
			return record, receipt, errors.New(failure.Reason + "\nprepare observed the runtime exit")
		}
		pod := "pod-b" + strconv.Itoa(batch)
		id := pod + "/containerd://runtime"
		record.Result.Actual.Instances = []serving.InstanceState{{ID: id, PodUID: pod, TargetID: prepare.Instance.ID}}
		metadata := &native.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "1.20", Providers: []string{"CPUExecutionProvider"},
			ModelFiles: []string{"/model/model.onnx"}, Device: "cpu", InitializedBatches: []int{batch}}
		if replaced, set := s.runtime[batch]; set {
			metadata = replaced
		}
		if metadata != nil {
			record.Result.Actual.Native = map[string]native.State{id: {Ready: true, Metadata: native.Metadata{Runtime: *metadata}}}
		}
	}
	return record, receipt, nil
}

func (s *sessionHarness) status(beo *edge.BatchEnergyOptimization) edge.BatchEnergyOptimizationStatus {
	s.t.Helper()
	if err := s.controller.store.client.Get(context.Background(), client.ObjectKeyFromObject(beo), beo); err != nil {
		s.t.Fatal(err)
	}
	return beo.Status
}

func (s *sessionHarness) result(beo *edge.BatchEnergyOptimization) (profilederivation.SessionResult, bool) {
	s.t.Helper()
	s.evidence.mu.Lock()
	raw, ok := s.evidence.objects[SessionResultPrefix+string(beo.UID)+"/result.json"]
	s.evidence.mu.Unlock()
	if !ok {
		return profilederivation.SessionResult{}, false
	}
	result, err := profilederivation.DecodeSessionResult(raw)
	if err != nil {
		s.t.Fatal(err)
	}
	return result, true
}

func sessionCondition(status edge.BatchEnergyOptimizationStatus) meta.Condition {
	for _, condition := range status.Conditions {
		if condition.Type == sessionConditionComplete {
			return condition
		}
	}
	return meta.Condition{}
}

func TestSessionMeasuresEveryBatchSizeAndStoresOneResult(t *testing.T) {
	s := newSessionHarness(t)
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	s.admit()
	status := s.status(beo)
	result, stored := s.result(beo)
	if !stored || status.Phase != string(edge.MeasurementPhaseComplete) || status.CompletedBatches != 3 || status.Result == nil ||
		status.Result.Key != SessionResultPrefix+string(beo.UID)+"/result.json" || !status.Terminal(beo.Generation) {
		t.Fatalf("status %+v result %+v stored %v", status, status.Result, stored)
	}
	for _, job := range s.jobs {
		if args := job.Spec.Template.Spec.Containers[0].Args; args[slices.Index(args, "--session")+1] != string(beo.UID) {
			t.Fatalf("load Job session identifier: %v", args)
		}
	}
	if condition := sessionCondition(status); condition.Status != meta.ConditionTrue {
		t.Fatalf("condition %+v", condition)
	}
	if len(result.Batches) != 3 || result.Batches[0].Batch != 1 || result.Batches[2].Batch != 4 || result.Stop != nil || result.RuntimeMetadata == nil ||
		result.Batches[0].PrepareCostKey == "" || result.Batches[0].PeakMemoryBytes != 700<<20 || len(result.Batches[0].Levels) != 3 || len(result.Batches[1].Levels) != 2 {
		t.Fatalf("result %+v", result)
	}
	want := []string{"power", "prepare-b1", "remove-b1", "prepare-b2", "remove-b2", "prepare-b4", "remove-b4"}
	if !slices.Equal(s.measured, want) || len(s.jobs) != 3 || s.holder() != "" {
		t.Fatalf("measured %v jobs %d holder %q", s.measured, len(s.jobs), s.holder())
	}
	jobs, _ := s.kube.BatchV1().Jobs("chill-system").List(context.Background(), meta.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Fatalf("load Jobs left: %d", len(jobs.Items))
	}
}

func TestSessionCapacityLimitKeepsTheBatchesBefore(t *testing.T) {
	for name, limit := range map[string]func(*sessionHarness){
		"prepare out of memory": func(s *sessionHarness) {
			s.prepareErr[4] = &serving.PreparationFailureObservation{Classification: serving.PreparationDeterministic,
				ReasonKey: "process-exit:firstfloor:OOMKilled:137", Reason: "firstfloor: OOMKilled (exit 137)"}
		},
		"allocation before any batch": func(s *sessionHarness) {
			s.tables[4] = sessionLevelTable{Levels: []sessionLevelWire{{Kind: profilederivation.LevelSaturation, WorkloadUID: "session-uid/4/saturation",
				StartedAt: s.origin, EndedAt: s.origin.Add(time.Second), SentInputs: 8, EndReason: sessionAllocationBeforeBatch, Detail: "CUDA out of memory"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSessionHarness(t)
			limit(s)
			beo := sessionRoot(sessionSettingsFixture())
			s.create(beo)
			s.admit()
			status := s.status(beo)
			result, stored := s.result(beo)
			if !stored || status.Phase != string(edge.MeasurementPhaseComplete) || status.CompletedBatches != 2 || status.Outcome != string(profilederivation.StopCapacityLimit) {
				t.Fatalf("status %+v stored %v", status, stored)
			}
			if len(result.Batches) != 2 || result.Stop == nil || result.Stop.Batch != 4 || result.Stop.Class != profilederivation.StopCapacityLimit ||
				result.Stop.Reason == "" || sessionCondition(status).Message != result.Stop.Reason {
				t.Fatalf("result batches %d stop %+v condition %+v", len(result.Batches), result.Stop, sessionCondition(status))
			}
			// The post-load window is observed on the last measured batch size.
			if !slices.Contains(s.measured, "prepare-post-b2") || !slices.Contains(s.measured, "remove-post-b2") || s.holder() != "" {
				t.Fatalf("measured %v holder %q", s.measured, s.holder())
			}
		})
	}
}

func TestSessionNotServableAtFirstBatchFailsWithoutResult(t *testing.T) {
	s := newSessionHarness(t)
	sentence := "two batches of 1 took 31.2 s, above the 30 s request lifetime"
	s.tables[1] = sessionLevelTable{Levels: []sessionLevelWire{{Kind: profilederivation.LevelSaturation, WorkloadUID: "session-uid/1/saturation",
		StartedAt: s.origin, EndedAt: s.origin.Add(32 * time.Second), SentInputs: 2, CompletedInputs: 2, CompletedBatches: 2, EndReason: sessionServiceOverLifetime, Detail: sentence}}}
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	s.admit()
	status := s.status(beo)
	if _, stored := s.result(beo); stored {
		t.Fatal("a session without a usable batch stored a result")
	}
	if status.Phase != string(edge.MeasurementPhaseFailed) || status.Outcome != string(profilederivation.StopNotServable) || status.Result != nil ||
		sessionCondition(status).Message != sentence || sessionCondition(status).Status != meta.ConditionFalse {
		t.Fatalf("status %+v", status)
	}
	if !slices.Equal(s.measured, []string{"power", "prepare-b1", "remove-b1"}) || s.holder() != "" {
		t.Fatalf("measured %v holder %q", s.measured, s.holder())
	}
}

func TestSessionLoadFailureCarriesTheOriginalSentence(t *testing.T) {
	s := newSessionHarness(t)
	sentence := "native batch failed: CUDA error: an illegal memory access was encountered"
	s.tables[2] = sessionLevelTable{Error: sentence, Levels: []sessionLevelWire{{Kind: profilederivation.LevelSaturation, WorkloadUID: "session-uid/2/saturation",
		StartedAt: s.origin, EndedAt: s.origin.Add(time.Second), SentInputs: 40, CompletedInputs: 20, CompletedBatches: 10, EndReason: "AllocationFailedAfterCompletedBatch"}}}
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	s.admit()
	status := s.status(beo)
	if _, stored := s.result(beo); stored || status.Phase != string(edge.MeasurementPhaseFailed) || status.Outcome != sessionOutcomeFailed ||
		sessionCondition(status).Message != sentence {
		t.Fatalf("status %+v", status)
	}
	if !slices.Equal(s.measured, []string{"power", "prepare-b1", "remove-b1", "prepare-b2", "remove-b2"}) || s.holder() != "" {
		t.Fatalf("instance not removed or Node not handed back: measured %v holder %q", s.measured, s.holder())
	}
	jobs, _ := s.kube.BatchV1().Jobs("chill-system").List(context.Background(), meta.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Fatalf("load Jobs left: %d", len(jobs.Items))
	}
}

// Every batch size runs on its own instance; the result keeps one runtime
// report, so the facts profile consumers read must be the same on all of them.
func TestSessionRuntimeFactDifferingAcrossInstancesFails(t *testing.T) {
	s := newSessionHarness(t)
	s.runtime = map[int]*native.RuntimeMetadata{2: {Runtime: "ort-cpu", RuntimeVersion: "1.20", Providers: []string{"CUDAExecutionProvider"},
		ModelFiles: []string{"/model/model.onnx"}, Device: "cpu", InitializedBatches: []int{2}}}
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	s.admit()
	status := s.status(beo)
	want := "the batch 2 instance reports providers [CUDAExecutionProvider], the batch 1 instance [CPUExecutionProvider]"
	if _, stored := s.result(beo); stored || status.Phase != string(edge.MeasurementPhaseFailed) || sessionCondition(status).Message != want {
		t.Fatalf("status %+v message %q", status, sessionCondition(status).Message)
	}
	if s.holder() != "" || !slices.Contains(s.measured, "remove-b2") {
		t.Fatalf("measured %v holder %q", s.measured, s.holder())
	}
}

func TestSessionNativeRowCountMismatchFailsWithTheNumbers(t *testing.T) {
	s := newSessionHarness(t)
	s.dropRow = "session-uid/2/mid"
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	s.admit()
	status := s.status(beo)
	if message := sessionCondition(status).Message; status.Phase != string(edge.MeasurementPhaseFailed) || !strings.Contains(message, "native rows 59 of 60") {
		t.Fatalf("status %+v message %q", status, message)
	}
	if _, stored := s.result(beo); stored || s.holder() != "" {
		t.Fatalf("result stored or Node held %q", s.holder())
	}
}

func TestSessionAdmissionRefusesWithTheReason(t *testing.T) {
	for name, scenario := range map[string]struct {
		session          *edge.BatchEnergyOptimizationSessionSpec
		change           func(*sessionHarness)
		outcome, message string
	}{
		"no session settings": {outcome: refusedSessionMissing, message: "measurement request has no session settings"},
		"meter undeclared": {session: sessionSettingsFixture(), outcome: MeterUndeclared, message: testNode.Name,
			change: func(s *sessionHarness) { s.controller.config.Meters = nil }},
		"unresolved": {session: sessionSettingsFixture(), outcome: refusedSessionUnresolved, message: "resolve static candidate: model not found",
			change: func(s *sessionHarness) {
				s.controller.config.Session.Prepare = func(context.Context, *edge.BatchEnergyOptimization) (SessionPreparation, error) {
					return SessionPreparation{}, errors.New("resolve static candidate: model not found\ncandidate report: absent")
				}
			}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSessionHarness(t)
			if scenario.change != nil {
				s.controller.mu.Lock()
				scenario.change(s)
				s.controller.mu.Unlock()
			}
			beo := sessionRoot(scenario.session)
			s.create(beo)
			s.admit()
			status := s.status(beo)
			if status.Phase != string(edge.MeasurementPhaseFailed) || status.Outcome != scenario.outcome || sessionCondition(status).Message != scenario.message ||
				!status.Terminal(beo.Generation) {
				t.Fatalf("status %+v", status)
			}
			if granted, _, _ := profilingrun.Admission(beo.Annotations); granted || s.holder() != "" || len(s.measured) != 0 {
				t.Fatalf("refused session granted %v holder %q measured %v", granted, s.holder(), s.measured)
			}
		})
	}
}

func TestSessionLoadJobSpec(t *testing.T) {
	beo := sessionRoot(sessionSettingsFixture())
	preparation := sessionPreparationFixture()
	config := SessionConfig{Image: "chill-server:1", Namespace: "chill-system", ServiceAccount: "session-load", NodeName: "chill-server-node"}
	for _, batch := range []int{1, 2} {
		job := sessionLoadJob(beo, testNode, sessionLoad{batch: batch, podUID: "pod-uid", servingNamespace: "serving", minimumSamples: 59,
			config: config, preparation: preparation, actor: sessionActorLimits{maxQueueRequests: 64, maxQueueBytes: 1 << 20}})
		pod := job.Spec.Template.Spec
		args := pod.Containers[0].Args
		value := func(flag string) string {
			i := slices.Index(args, flag)
			if i < 0 || i+1 >= len(args) {
				return ""
			}
			return args[i+1]
		}
		if *job.Spec.BackoffLimit != 0 || pod.NodeName != "chill-server-node" || pod.ServiceAccountName != "session-load" || pod.Containers[0].Image != "chill-server:1" ||
			job.Namespace != "chill-system" || job.Labels[loadNodeLabel] != testNode.UID || pod.RestartPolicy != core.RestartPolicyNever {
			t.Fatalf("batch %d Job %+v", batch, job)
		}
		if args[0] != "session-load" || value("--session") != "session-uid" || value("--batch") != strconv.Itoa(batch) ||
			value("--native-endpoint") != "http://chill-executor-pod-uid.serving:8080" || value("--min-samples") != "59" || value("--model") != "mobilenet" ||
			value("--fields") != "input" || value("--dataset-dir") != "/data" || value("--level-limit") != "600s" || value("--meter-lag") != "0.5s" ||
			value("--request-lifetime") != "30s" || value("--delivery-margin") != "100ms" || value("--request-upper-bytes") != "1024" {
			t.Fatalf("batch %d args %v", batch, args)
		}
		if slices.Contains(args, "--sparse") != (batch == 1) {
			t.Fatalf("batch %d sparse level args %v", batch, args)
		}
		init := pod.InitContainers
		if len(init) != 1 || init[0].Image != "dataset:1" || init[0].Command[len(init[0].Command)-1] != "/datasets/imagenet/profiling" {
			t.Fatalf("dataset init container %+v", init)
		}
	}
}

func TestSessionRestartFailsAndHandsBack(t *testing.T) {
	s := newSessionHarness(t)
	beo := sessionRoot(sessionSettingsFixture())
	beo.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Now())}
	beo.Status.Phase = string(edge.MeasurementPhaseMeasuring)
	s.create(beo)
	s.setNode(func(n *core.Node) {
		n.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: string(beo.UID), Effect: core.TaintEffectNoSchedule}}
	})
	s.admit()
	status := s.status(beo)
	if status.Phase != string(edge.MeasurementPhaseFailed) || status.Outcome != sequenceRestartReason || !status.Terminal(beo.Generation) || s.holder() != "" {
		t.Fatalf("status %+v holder %q", status, s.holder())
	}
	if len(s.measured) != 0 {
		t.Fatalf("a cut-off session measured again: %v", s.measured)
	}
}

// sessionObservedRuntime is a runtime whose observation log stores what the
// observer passes it, as in ChillServer.
func sessionObservedRuntime(t *testing.T) (*serving.Runtime, *observation.ObservationLog) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: managerArchive{t.TempDir()},
		OccupancyTail: 3 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	rt, err := serving.NewRuntime(ctx, serving.RuntimeConfig{ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	return rt, log
}

// emitSessionBatches passes the progress lines native prints for batches of
// two requests of one level to the runtime, as the observer's log follow does:
// one line per request and phase, the batch's service only on finished. skew
// shifts the executor clock.
func emitSessionBatches(t *testing.T, rt *serving.Runtime, uid string, start time.Time, batches int, skew time.Duration) {
	t.Helper()
	const service = 20 * time.Millisecond
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	var lastEnd time.Time
	for b := range batches {
		received := start.Add(skew + time.Duration(b)*50*time.Millisecond)
		running := received.Add(5 * time.Millisecond)
		finished := running.Add(service)
		sample := &native.ServiceSample{ServiceDurationNanos: service.Nanoseconds(), TrialID: uid}
		if !lastEnd.IsZero() {
			gap := running.Sub(lastEnd).Nanoseconds()
			sample.PrecedingIdleNanos = &gap
		}
		lastEnd = finished
		for m := range 2 {
			queued := received.Add(time.Duration(m) * time.Millisecond)
			base := native.Progress{RequestID: fmt.Sprintf("saturation-%d", 2*b+m), WorkloadUID: uid, Definition: "42", ProfileID: "profile", DecisionID: "decision"}
			lines := []native.Progress{base, base, base}
			lines[0].Phase, lines[0].At, lines[0].QueueMS, lines[0].NativeElapsedMS = "queued", queued, 0, 0
			lines[1].Phase, lines[1].At, lines[1].BatchID, lines[1].BatchSize = "running", running, uint64(b+1), 2
			lines[1].QueueMS, lines[1].NativeElapsedMS = ms(running.Sub(queued)), ms(running.Sub(queued))
			lines[2].Phase, lines[2].At, lines[2].BatchID, lines[2].BatchSize, lines[2].ExecutionComplete = "finished", finished, uint64(b+1), 2, true
			lines[2].QueueMS, lines[2].ServiceMS, lines[2].BackendMS, lines[2].NativeElapsedMS = ms(running.Sub(queued)), ms(service), ms(service), ms(finished.Sub(queued))
			lines[2].Outcome, lines[2].ServiceSample, lines[2].Units = "completed", sample, 1
			for _, line := range lines {
				raw, err := json.Marshal(line)
				if err != nil {
					t.Fatal(err)
				}
				progress, err := servingnative.ParseProgress(append([]byte("2026-11-14T00:00:00Z CHILL_PROGRESS "), raw...))
				if err != nil {
					t.Fatal(err)
				}
				if err := rt.ObserveBackend("pod-b2/containerd://runtime", progress); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// The session's reads join a request's per-phase supplement rows before
// folding per batch: no single stored row has running, finished and service.
// Rows are selected by the level identifier, whatever the executor clock
// reads against ChillServer's.
func TestSessionReadsJoinNativeProgressLines(t *testing.T) {
	for _, skew := range []time.Duration{0, 30 * time.Second, -30 * time.Second} {
		t.Run(skew.String(), func(t *testing.T) {
			rt, log := sessionObservedRuntime(t)
			uid := "session-uid/2/saturation"
			start := time.Now().UTC()
			emitSessionBatches(t, rt, uid, start, 3, skew)
			byLevel, err := observedSessionReads(log).native(context.Background(), uid, start)
			rows := byLevel[uid]
			if err != nil || len(rows) != 3 {
				t.Fatalf("native batch rows %d err %v", len(rows), err)
			}
			for i, row := range rows {
				running := start.Add(skew + time.Duration(i)*50*time.Millisecond + 5*time.Millisecond)
				if row.BatchID != uint64(i+1) || row.BatchSize != 2 || !row.StartedAt.Equal(running) || !row.FinishedAt.Equal(running.Add(20*time.Millisecond)) ||
					row.ServiceDurationNanos != (20*time.Millisecond).Nanoseconds() || !row.LatestEnqueuedAt.Equal(running.Add(-4*time.Millisecond)) ||
					(row.PrecedingIdleNanos == nil) != (i == 0) || i > 0 && *row.PrecedingIdleNanos != (30*time.Millisecond).Nanoseconds() {
					t.Fatalf("batch %d row %+v", i, row)
				}
			}
		})
	}
}

func TestSessionQueueCapacityStopsBeforePreparing(t *testing.T) {
	for _, upper := range []int64{200 << 10, 600 << 10} {
		t.Run(strconv.FormatInt(upper, 10), func(t *testing.T) {
			s := newSessionHarness(t)
			settings := sessionSettingsFixture()
			settings.FramedDecodedRequestUpperBytes = upper
			beo := sessionRoot(settings)
			s.create(beo)
			s.admit()
			status := s.status(beo)
			if status.Outcome != string(profilederivation.StopCapacityLimit) ||
				!strings.Contains(sessionCondition(status).Message, "queue limit") {
				t.Fatalf("status %+v", status)
			}
			if upper == 600<<10 {
				if status.Phase != string(edge.MeasurementPhaseFailed) || slices.Contains(s.measured, "prepare-b1") {
					t.Fatalf("status %+v operations %v", status, s.measured)
				}
			} else if result, ok := s.result(beo); !ok || len(result.Batches) != 2 ||
				slices.Contains(s.measured, "prepare-b4") {
				t.Fatalf("result %+v stored %v operations %v", result, ok, s.measured)
			}
		})
	}
}

func TestSessionMissingRuntimeFailsBeforeLoadAndRemovesInstance(t *testing.T) {
	s := newSessionHarness(t)
	s.runtime = map[int]*native.RuntimeMetadata{1: nil}
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	s.admit()
	status := s.status(beo)
	if status.Phase != string(edge.MeasurementPhaseFailed) ||
		!strings.Contains(sessionCondition(status).Message, "instance reported no runtime metadata") ||
		len(s.jobs) != 0 || !slices.Contains(s.measured, "remove-b1") {
		t.Fatalf("status %+v jobs %d operations %v", status, len(s.jobs), s.measured)
	}
}

func TestSessionRefusesLoadNodeAsMeasurementNode(t *testing.T) {
	s := newSessionHarness(t)
	s.controller.config.Session.NodeName = testNode.Name
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	s.admit()
	status := s.status(beo)
	if status.Phase != string(edge.MeasurementPhaseFailed) ||
		!strings.Contains(sessionCondition(status).Message, "load generator runs on this Node") ||
		len(s.measured) != 0 {
		t.Fatalf("status %+v operations %v", status, s.measured)
	}
}

func TestSessionResolutionTransportFailureRemainsPending(t *testing.T) {
	s := newSessionHarness(t)
	prepare := s.controller.config.Session.Prepare
	s.controller.config.Session.Prepare = func(context.Context, *edge.BatchEnergyOptimization) (SessionPreparation, error) {
		return SessionPreparation{}, &SessionResolutionRetry{Err: errors.New("get ModelSpec: connection reset by peer")}
	}
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	s.admit()
	if status := s.status(beo); status.Terminal(beo.Generation) || len(s.measured) != 0 ||
		sessionCondition(status).Message != "get ModelSpec: connection reset by peer" {
		t.Fatalf("status %+v operations %v", status, s.measured)
	}
	s.controller.config.Session.Prepare = prepare
	s.admit()
	if status := s.status(beo); status.Phase != string(edge.MeasurementPhaseComplete) {
		t.Fatalf("status after retry %+v", status)
	}
}

func TestSessionCancellationBeforeDerivationPublishesNothing(t *testing.T) {
	s := newSessionHarness(t)
	beo := sessionRoot(sessionSettingsFixture())
	s.create(beo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	power := s.controller.sessionReads.power
	s.controller.sessionReads.power = func(ctx context.Context, meter servingops.MeterDeclaration, from, to time.Time) ([]profilederivation.Sample, error) {
		rows, err := power(ctx, meter, from, to)
		cancel()
		return rows, err
	}
	plan := sessionPlan{preparation: sessionPreparationFixture(), meter: s.controller.config.Meters[0], powered: true, confidence: .95}
	plan.minimumSamples, _ = native.MinimumServiceSamples(plan.preparation.SLOPercentile)
	err := s.controller.runSession(ctx, beo, testNode, plan, s.controller.config)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error %v", err)
	}
	if status := s.status(beo); status.Terminal(beo.Generation) || status.Result != nil {
		t.Fatalf("canceled run published status %+v", status)
	}
	if _, stored := s.result(beo); stored {
		t.Fatal("canceled derivation stored a result")
	}
}

func TestSessionMidLifetimeExpiryKeepsLevelAndLaterBatches(t *testing.T) {
	for _, enoughPower := range []bool{false, true} {
		t.Run(strconv.FormatBool(enoughPower), func(t *testing.T) {
			s := newSessionHarness(t)
			const sentence = "native inference HTTP 408: request lifetime expired"
			table := s.tables[1]
			level := &table.Levels[1]
			level.EndReason, level.Detail = sessionRequestLifetimeExpired, sentence
			if !enoughPower {
				level.EndedAt = level.StartedAt.Add(100 * time.Millisecond)
				level.SentInputs, level.CompletedInputs, level.CompletedBatches = 1, 0, 0
				s.native[level.WorkloadUID] = nil
			}
			s.tables[1] = table
			beo := sessionRoot(sessionSettingsFixture())
			s.create(beo)
			s.admit()
			result, stored := s.result(beo)
			if !stored || len(result.Batches) != 3 || len(result.Batches[0].Levels) != 3 ||
				result.Batches[0].Levels[1].EndReason != sessionRequestLifetimeExpired ||
				result.Batches[0].Levels[1].Detail != sentence {
				t.Fatalf("result %+v stored %v", result, stored)
			}
			mid := result.Batches[0].Levels[1]
			if (mid.MeanWatts != nil) != enoughPower || enoughPower && mid.PowerSamples == 0 {
				t.Fatalf("stored power eligibility %+v enough power %v", mid, enoughPower)
			}
		})
	}
}

func BenchmarkSessionNativeBatchFold(b *testing.B) {
	for _, count := range []int{75_000, 150_000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			start := time.Unix(1000, 0)
			end := start.Add(time.Millisecond)
			rows := make([]serving.RequestRow, count*4)
			for i := range rows {
				queued := start.Add(time.Duration(i%4) * time.Nanosecond)
				rows[i] = serving.RequestRow{Kind: serving.RequestRowNative, RequestID: strconv.Itoa(i), BatchID: uint64(i/4 + 1),
					BatchSize: 4, NativeQueuedAt: &queued, NativeRunningAt: &start, NativeFinishedAt: &end, ServiceDurationNanos: time.Millisecond.Nanoseconds()}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				join := serving.NewNativeBatchJoin()
				for _, row := range rows {
					join.Add(row)
				}
				folded, err := join.Batches()
				if err != nil || len(folded) != count {
					b.Fatalf("folded %d batches: %v", len(folded), err)
				}
			}
			b.ReportMetric(float64(len(rows)), "rows/op")
		})
	}
}
