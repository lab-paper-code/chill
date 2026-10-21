package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAttemptMaterializerProjectsBothDirectionsAndRecovers(t *testing.T) {
	tests := []struct {
		name       string
		direction  int
		attempt    int32
		targetMode string
	}{
		{name: "low to high", direction: 0, attempt: 1, targetMode: "high"},
		{name: "high to low", direction: 1, attempt: 2, targetMode: "low"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, test.direction)
			name := fmt.Sprintf("transition-transition-cost-uid-d%d-a%d", test.direction, test.attempt)

			progress, err := fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, test.direction, test.attempt,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != AttemptStageObserverStarting || progress.Job == nil || progress.Job.Name != name ||
				fixture.stores.calls != 1 {
				t.Fatalf("first progress = %#v, store calls = %d", progress, fixture.stores.calls)
			}
			var payload attemptPayload
			if err := json.Unmarshal([]byte(envValue(
				progress.Job.Spec.Template.Spec.Containers[0].Env, transitionAttemptJSONEnvName,
			)), &payload); err != nil {
				t.Fatal(err)
			}
			wantRawKey := fmt.Sprintf(
				"power-mode-transition-costs/transition-cost-uid/d%d-a%d/raw.json", test.direction, test.attempt,
			)
			if payload.RawLocation.Store != attemptEvidenceStore || payload.RawLocation.Key != wantRawKey {
				t.Fatalf("raw location = %#v", payload.RawLocation)
			}
			requireFinalLocation(t, fixture.measurement, test.direction, test.attempt,
				strings.TrimSuffix(wantRawKey, "raw.json")+"final.json")
			if mode := nodePowerMode(t, fixture); mode == test.targetMode {
				t.Fatal("power mode was requested before the observer was Ready")
			}

			setJobReady(t, fixture.client, progress.Job)
			progress, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, test.direction, test.attempt,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != AttemptStageActuationRunning || progress.Job == nil || progress.Job.Name != name {
				t.Fatalf("actuation progress = %#v", progress)
			}
			if mode := nodePowerMode(t, fixture); mode != test.targetMode {
				t.Fatalf("requested power mode = %q, want %q", mode, test.targetMode)
			}
			progress, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, test.direction, test.attempt,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != AttemptStageActuationRunning || fixture.stores.calls != 1 ||
				objectCount(t, fixture.client, fixture.measurement.Namespace, &batchv1.JobList{}) != 1 {
				t.Fatalf("recovery = %#v; store calls = %d", progress, fixture.stores.calls)
			}
		})
	}
}

func TestAttemptMaterializerWaitsForRequestedModeAfterObserverCompletes(t *testing.T) {
	for _, test := range []struct {
		condition  batchv1.JobConditionType
		wantStage  AttemptStage
		wantReason string
	}{
		{condition: batchv1.JobComplete, wantStage: AttemptStageActuationRunning},
		{condition: batchv1.JobFailed, wantStage: AttemptStageTerminal, wantReason: AttemptReasonObserverFailed},
	} {
		t.Run(string(test.condition), func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, 0)
			progress := advanceToRequested(t, fixture)
			setAgentTransition(t, fixture, metadata.PowerModePending, false)
			setJobCondition(t, fixture.client, progress.Job, test.condition)

			progress, err := fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != test.wantStage || progress.Reason != test.wantReason ||
				progress.Job == nil || fixture.stores.calls != 1 ||
				objectCount(t, fixture.client, fixture.measurement.Namespace, &batchv1.JobList{}) != 1 {
				t.Fatalf("retained attempt progress = %#v, store calls = %d", progress, fixture.stores.calls)
			}
		})
	}
}

func TestAttemptMaterializerFailsWhenObserverTerminatesBeforeActuation(t *testing.T) {
	for _, test := range []struct {
		condition batchv1.JobConditionType
		reason    string
	}{
		{condition: batchv1.JobComplete, reason: AttemptReasonObserverEndedEarly},
		{condition: batchv1.JobFailed, reason: AttemptReasonObserverFailed},
	} {
		t.Run(string(test.condition), func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, 0)
			progress, err := fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			setJobCondition(t, fixture.client, progress.Job, test.condition)

			progress, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != AttemptStageTerminal || progress.Reason != test.reason || progress.Job == nil ||
				nodePowerMode(t, fixture) != fixture.prepared.Directions[0].FrozenContract.SourceState.PowerMode {
				t.Fatalf("terminal-before-actuation progress = %#v", progress)
			}
		})
	}
}

func requireFinalLocation(t *testing.T, measurement *edgev1alpha1.PowerModeTransitionCost,
	direction int, attempt int32, want string,
) {
	t.Helper()
	selection, err := selectAttempt(measurement, attemptEvidenceStore, direction, attempt)
	if err != nil || selection.finalLocation.Key != want {
		t.Fatalf("final location = %#v, error = %v", selection.finalLocation, err)
	}
}

func TestAttemptMaterializerRejectsInvalidSelectionBeforeMaterialization(t *testing.T) {
	tests := []struct {
		name      string
		direction int
		attempt   int32
	}{
		{name: "negative direction", direction: -1, attempt: 1},
		{name: "third direction", direction: 2, attempt: 1},
		{name: "zero attempt", direction: 0, attempt: 0},
		{name: "past frozen limit", direction: 0, attempt: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, 0)
			if _, err := fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, test.direction, test.attempt,
			); err == nil {
				t.Fatal("Advance() unexpectedly succeeded")
			}
			if fixture.stores.calls != 0 {
				t.Fatalf("invalid selection crossed a material boundary: store=%d", fixture.stores.calls)
			}
		})
	}
}

func TestAttemptMaterializerFailsClosedOnUnconfirmedSource(t *testing.T) {
	tests := []struct {
		name   string
		alter  func(*corev1.Node)
		reason string
	}{
		{name: "never requested", reason: AttemptReasonSourceStateUnknown, alter: func(node *corev1.Node) {
			node.Labels = nil
			delete(node.Annotations, metadata.PowerModeTransition)
		}},
		{name: "pending", reason: AttemptReasonSourceStateUnknown, alter: func(node *corev1.Node) {
			node.Labels[metadata.PowerModeState] = metadata.PowerModePending
		}},
		{name: "changed since", reason: AttemptReasonSourceStateUnknown, alter: func(node *corev1.Node) {
			node.Annotations[metadata.PowerModeGeneration] = "4"
		}},
		{name: "wrong read-back", reason: AttemptReasonSourceStateMismatch, alter: func(node *corev1.Node) {
			node.Annotations[metadata.PowerActuator] = `{"kind":"JetsonNVPModel","jetsonNVPModel":{"id":9}}`
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, 0, test.alter)
			progress, err := fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != AttemptStageTerminal || progress.Reason != test.reason || progress.Job != nil ||
				fixture.stores.calls != 0 {
				t.Fatalf("progress = %#v, store calls = %d", progress, fixture.stores.calls)
			}
		})
	}
}

func TestAttemptMaterializerRecoversConcurrentObserverJob(t *testing.T) {
	fixture := newAttemptMaterializerFixture(t, 0)
	raceClient := &attemptCreationRaceClient{Client: fixture.client, target: raceObserverJob}
	fixture.materializer.client = raceClient

	progress, err := fixture.materializer.Advance(
		context.Background(), fixture.measurement, fixture.prepared, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Stage != AttemptStageObserverStarting || progress.Job == nil ||
		progress.Job.UID != "concurrent-winner" || raceClient.createCalls != 1 || fixture.stores.calls != 1 {
		t.Fatalf(
			"concurrent Job progress = %#v, creates = %d, store calls = %d",
			progress, raceClient.createCalls, fixture.stores.calls,
		)
	}

	if _, err := fixture.materializer.Advance(
		context.Background(), fixture.measurement, fixture.prepared, 0, 1,
	); err != nil {
		t.Fatal(err)
	}
	if raceClient.createCalls != 1 || fixture.stores.calls != 1 {
		t.Fatalf("retained Job was recreated: creates = %d, store calls = %d", raceClient.createCalls, fixture.stores.calls)
	}
}

func TestAttemptMaterializerRequestsOnlyOnItsOwnGrantedNode(t *testing.T) {
	fixture := newAttemptMaterializerFixture(t, 0, func(node *corev1.Node) {
		node.Spec.Taints[0].Value = "other-measurement"
	})
	progress, err := fixture.materializer.Advance(
		context.Background(), fixture.measurement, fixture.prepared, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	setJobReady(t, fixture.client, progress.Job)

	_, err = fixture.materializer.Advance(
		context.Background(), fixture.measurement, fixture.prepared, 0, 1,
	)
	if err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatalf("Advance() error = %v, want not granted", err)
	}
	if mode := nodePowerMode(t, fixture); mode != fixture.prepared.Directions[0].FrozenContract.SourceState.PowerMode {
		t.Fatalf("power mode = %q written on a Node granted elsewhere", mode)
	}
}

func TestAttemptMaterializerPreservesConcurrentWinnerReloadErrors(t *testing.T) {
	reloadErr := errors.New("reload failed")
	for _, test := range []struct {
		name        string
		target      attemptCreationRaceTarget
		prepare     func(*testing.T, attemptMaterializerFixture)
		wantContext string
	}{
		{
			name: "observer Job", target: raceObserverJob,
			prepare:     func(*testing.T, attemptMaterializerFixture) {},
			wantContext: "get transition attempt Job",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, 0)
			test.prepare(t, fixture)
			selection, err := selectAttempt(fixture.measurement, fixture.prepared.EvidenceStore, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			raceClient := &attemptCreationRaceClient{
				Client: fixture.client, target: test.target, reloadErr: reloadErr,
			}
			fixture.materializer.client = raceClient

			_, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			wantError := fmt.Sprintf(
				"%s %s/%s: %s", test.wantContext, fixture.measurement.Namespace, selection.name, reloadErr,
			)
			if !errors.Is(err, reloadErr) || err.Error() != wantError {
				t.Fatalf("Advance() error = %v, want %q wrapping %q", err, wantError, reloadErr)
			}
			if raceClient.createCalls != 1 || fixture.stores.calls != 1 {
				t.Fatalf(
					"race calls: creates = %d, store = %d", raceClient.createCalls, fixture.stores.calls,
				)
			}
		})
	}
}

func TestAttemptMaterializerReturnsTerminalPairForFinalization(t *testing.T) {
	for _, conditionType := range []batchv1.JobConditionType{batchv1.JobComplete, batchv1.JobFailed} {
		for _, state := range []string{metadata.PowerModeSuccess, metadata.PowerModeFailed} {
			t.Run(string(conditionType)+"/"+state, func(t *testing.T) {
				fixture := newAttemptMaterializerFixture(t, 0)
				progress := advanceToRequested(t, fixture)
				setAgentTransition(t, fixture, state, true)
				setJobCondition(t, fixture.client, progress.Job, conditionType)

				progress, err := fixture.materializer.Advance(
					context.Background(), fixture.measurement, fixture.prepared, 0, 1,
				)
				if err != nil {
					t.Fatal(err)
				}
				if progress.Stage != AttemptStageFinalizationReady || progress.Job == nil {
					t.Fatalf("finalization progress = %#v", progress)
				}
			})
		}
	}
}

type attemptMaterializerFixture struct {
	measurement  *edgev1alpha1.PowerModeTransitionCost
	prepared     PreparedMeasurement
	client       client.Client
	materializer *AttemptMaterializer
	stores       *recordingAttemptStoreResolver
}

type attemptCreationRaceTarget string

const raceObserverJob attemptCreationRaceTarget = "observer-job"

type attemptCreationRaceClient struct {
	client.Client
	target      attemptCreationRaceTarget
	reloadErr   error
	raced       bool
	createCalls int
}

func (c *attemptCreationRaceClient) Create(
	ctx context.Context,
	object client.Object,
	options ...client.CreateOption,
) error {
	if !c.matches(object) {
		return c.Client.Create(ctx, object, options...)
	}
	c.createCalls++
	if c.raced {
		return c.Client.Create(ctx, object, options...)
	}
	c.raced = true
	winner := object.DeepCopyObject().(client.Object)
	winner.SetUID(types.UID("concurrent-winner"))
	if err := c.Client.Create(ctx, winner, options...); err != nil {
		return err
	}
	return apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, object.GetName())
}

func (c *attemptCreationRaceClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	object client.Object,
	options ...client.GetOption,
) error {
	if c.raced && c.reloadErr != nil && c.matches(object) {
		return c.reloadErr
	}
	return c.Client.Get(ctx, key, object, options...)
}

func (c *attemptCreationRaceClient) matches(object client.Object) bool {
	_, job := object.(*batchv1.Job)
	return job && c.target == raceObserverJob
}

// newAttemptMaterializerFixture holds the target Node, granted to the
// measurement, settled in the source mode of direction.
func newAttemptMaterializerFixture(t *testing.T, direction int, alter ...func(*corev1.Node)) attemptMaterializerFixture {
	t.Helper()
	measurement, prepared, scheme := preparationRecordFixture(t)
	measurement.Spec.AttemptLimit = 3
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	node := sourceNode(t, measurement, prepared, direction)
	for _, change := range alter {
		change(node)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&batchv1.Job{}).WithObjects(node).Build()
	stores := &recordingAttemptStoreResolver{binding: storekube.Binding{
		Store: attemptEvidenceStore, Endpoint: "http://store", Bucket: "bucket", Region: "region",
		CredentialsSecretName: "credentials",
	}}
	_, options, _ := validAttemptJobInputs(t)
	options.ActiveDeadlineOverhead = 20 * time.Minute
	materializer, err := NewAttemptMaterializer(kubeClient, kubeClient, scheme, stores, options)
	if err != nil {
		t.Fatal(err)
	}
	materializer.now = func() time.Time { return time.Unix(150, 0) }
	return attemptMaterializerFixture{
		measurement: measurement, prepared: prepared, client: kubeClient,
		materializer: materializer, stores: stores,
	}
}

func sourceNode(
	t *testing.T,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	prepared PreparedMeasurement,
	direction int,
) *corev1.Node {
	t.Helper()
	mode := prepared.Directions[direction].FrozenContract.SourceState.PowerMode
	readBack, err := json.Marshal(prepared.Directions[1-direction].Actuator)
	if err != nil {
		t.Fatal(err)
	}
	settledAt := time.Unix(100, 0).UTC()
	transition, err := json.Marshal(metadata.PowerModeTransitionRecord{
		PowerMode: mode, Generation: 3, VerifiedAt: &settledAt, ReadyAt: &settledAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: prepared.TargetNode.Name, UID: types.UID(prepared.TargetNode.UID),
			Labels: map[string]string{metadata.PowerMode: mode, metadata.PowerModeState: metadata.PowerModeSuccess},
			Annotations: map[string]string{
				metadata.PowerActuator: string(readBack), metadata.PowerModeGeneration: "3",
				metadata.PowerModeTransition: string(transition),
			},
		},
		Spec: corev1.NodeSpec{Taints: []corev1.Taint{{
			Key: profilingrun.MeasurementTaintKey, Value: string(measurement.UID), Effect: corev1.TaintEffectNoSchedule,
		}}},
	}
}

// advanceToRequested creates the d0-a1 observer Job, makes it Ready, and
// lets the materializer request the target mode.
func advanceToRequested(t *testing.T, fixture attemptMaterializerFixture) AttemptProgress {
	t.Helper()
	progress, err := fixture.materializer.Advance(context.Background(), fixture.measurement, fixture.prepared, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	setJobReady(t, fixture.client, progress.Job)
	progress, err = fixture.materializer.Advance(context.Background(), fixture.measurement, fixture.prepared, 0, 1)
	if err != nil || progress.Stage != AttemptStageActuationRunning {
		t.Fatalf("request progress = %#v, error = %v", progress, err)
	}
	return progress
}

// setAgentTransition plays the Node agent on the requested d0 target mode:
// fenced and pending, settled, or failed.
func setAgentTransition(t *testing.T, fixture attemptMaterializerFixture, state string, verified bool) {
	t.Helper()
	direction := fixture.prepared.Directions[0]
	node := &corev1.Node{}
	if err := fixture.client.Get(context.Background(), client.ObjectKey{Name: fixture.prepared.TargetNode.Name}, node); err != nil {
		t.Fatal(err)
	}
	appliedAt, readyAt := time.Unix(140, 0).UTC(), time.Unix(149, 0).UTC()
	record := metadata.PowerModeTransitionRecord{
		PowerMode: direction.FrozenContract.TargetState.PowerMode, Generation: 3, ApplyStartedAt: &appliedAt,
	}
	switch {
	case state == metadata.PowerModeFailed:
		record.Failure = "apply failed"
	case verified:
		readBack, err := json.Marshal(direction.Actuator)
		if err != nil {
			t.Fatal(err)
		}
		record.Generation, record.VerifiedAt, record.SettlingStartedAt, record.ReadyAt = 4, &readyAt, &readyAt, &readyAt
		node.Annotations[metadata.PowerActuator] = string(readBack)
		node.Annotations[metadata.PowerModeGeneration] = "4"
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	node.Annotations[metadata.PowerModeTransition] = string(raw)
	node.Labels[metadata.PowerModeState] = state
	if err := fixture.client.Update(context.Background(), node); err != nil {
		t.Fatal(err)
	}
}

func nodePowerMode(t *testing.T, fixture attemptMaterializerFixture) string {
	t.Helper()
	node := &corev1.Node{}
	if err := fixture.client.Get(context.Background(), client.ObjectKey{Name: fixture.prepared.TargetNode.Name}, node); err != nil {
		t.Fatal(err)
	}
	return node.Labels[metadata.PowerMode]
}

func setJobReady(t *testing.T, kubeClient client.Client, job *batchv1.Job) {
	t.Helper()
	job = job.DeepCopy()
	job.Status.Ready = ptr.To[int32](1)
	if err := kubeClient.Status().Update(context.Background(), job); err != nil {
		t.Fatal(err)
	}
}

func setJobCondition(
	t *testing.T,
	kubeClient client.Client,
	job *batchv1.Job,
	conditionType batchv1.JobConditionType,
) {
	t.Helper()
	job = job.DeepCopy()
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
		Type: conditionType, Status: corev1.ConditionTrue,
	})
	if err := kubeClient.Status().Update(context.Background(), job); err != nil {
		t.Fatal(err)
	}
}

func objectCount(t *testing.T, kubeClient client.Client, namespace string, objects client.ObjectList) int {
	t.Helper()
	if err := kubeClient.List(context.Background(), objects, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	switch listed := objects.(type) {
	case *batchv1.JobList:
		return len(listed.Items)
	default:
		t.Fatalf("unsupported object list %T", objects)
		return 0
	}
}

type recordingAttemptStoreResolver struct {
	binding storekube.Binding
	calls   int
}

func (r *recordingAttemptStoreResolver) Resolve(context.Context) (storekube.Binding, error) {
	r.calls++
	if r.calls > 1 {
		return storekube.Binding{}, errors.New("Evidence Store binding was resolved more than once")
	}
	return r.binding, nil
}
