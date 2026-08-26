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
		name          string
		direction     int
		attempt       int32
		targetMode    string
		targetModelID int32
		policy        string
	}{
		{name: "low to high", direction: 0, attempt: 1, targetMode: "high", targetModelID: 2, policy: "to-high"},
		{name: "high to low", direction: 1, attempt: 2, targetMode: "low", targetModelID: 1, policy: "to-low"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, confirmedSourceActuation(t, test.direction, time.Unix(100, 0)))
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

			setJobReady(t, fixture.client, progress.Job)
			progress, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, test.direction, test.attempt,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != AttemptStageActuationRunning || progress.NodeActuation == nil ||
				progress.NodeActuation.Name != name || progress.NodeActuation.Spec.PowerMode != test.targetMode ||
				progress.NodeActuation.Spec.PolicyName != test.policy ||
				progress.NodeActuation.Spec.Node != fixture.prepared.TargetNode ||
				progress.NodeActuation.Spec.DeviceClass != fixture.prepared.DeviceClass ||
				progress.NodeActuation.Spec.Actuator.JetsonNVPModel == nil ||
				progress.NodeActuation.Spec.Actuator.JetsonNVPModel.ID != test.targetModelID {
				t.Fatalf("actuation progress = %#v", progress)
			}
			policy := fixture.prepared.Directions[test.direction].ActuationPolicy.Policy
			if progress.NodeActuation.Spec.SettlingDurationSeconds != int32(policy.SettlingDurationSeconds) ||
				progress.NodeActuation.Spec.MaxActuationDurationSeconds != int32(policy.MaxActuationDurationSeconds) ||
				!progress.NodeActuation.Spec.StageRequestedAt.Time.Equal(time.Unix(150, 0).UTC()) ||
				!progress.NodeActuation.Spec.Deadline.Time.Equal(time.Unix(150, 0).UTC().Add(
					time.Duration(policy.MaxActuationDurationSeconds)*time.Second,
				)) || !metav1.IsControlledBy(progress.NodeActuation, fixture.measurement) {
				t.Fatalf("actuation policy projection = %#v", progress.NodeActuation)
			}
			if _, err := fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, test.direction, test.attempt,
			); err != nil {
				t.Fatal(err)
			}
			if fixture.stores.calls != 1 ||
				objectCount(t, fixture.client, fixture.measurement.Namespace, &batchv1.JobList{}) != 1 ||
				objectCount(t, fixture.client, fixture.measurement.Namespace, &edgev1alpha1.NodeActuationList{}) != 2 {
				t.Fatalf("recovery duplicated attempt resources; store calls = %d", fixture.stores.calls)
			}
		})
	}
}

func TestAttemptMaterializerWaitsForRetainedActuationAfterObserverCompletes(t *testing.T) {
	for _, test := range []struct {
		condition  batchv1.JobConditionType
		wantStage  AttemptStage
		wantReason string
	}{
		{condition: batchv1.JobComplete, wantStage: AttemptStageActuationRunning},
		{condition: batchv1.JobFailed, wantStage: AttemptStageTerminal, wantReason: AttemptReasonObserverFailed},
	} {
		t.Run(string(test.condition), func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(
				t,
				confirmedSourceActuation(t, 0, time.Unix(100, 0)),
			)
			progress, err := fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			setJobReady(t, fixture.client, progress.Job)
			progress, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			actuation := progress.NodeActuation.DeepCopy()
			actuation.Status = edgev1alpha1.NodeActuationStatus{
				ObservedGeneration: actuation.Generation,
				Phase:              edgev1alpha1.NodeActuationPhaseSettling,
			}
			if err := fixture.client.Status().Update(context.Background(), actuation); err != nil {
				t.Fatal(err)
			}
			setJobCondition(t, fixture.client, progress.Job, test.condition)

			progress, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != test.wantStage || progress.Reason != test.wantReason ||
				progress.Job == nil || progress.NodeActuation == nil ||
				progress.Job.Name != progress.NodeActuation.Name || fixture.stores.calls != 1 ||
				objectCount(t, fixture.client, fixture.measurement.Namespace, &batchv1.JobList{}) != 1 ||
				objectCount(t, fixture.client, fixture.measurement.Namespace, &edgev1alpha1.NodeActuationList{}) != 2 {
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
			fixture := newAttemptMaterializerFixture(
				t,
				confirmedSourceActuation(t, 0, time.Unix(100, 0)),
			)
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
			if progress.Stage != AttemptStageTerminal || progress.Reason != test.reason ||
				progress.Job == nil || progress.NodeActuation != nil ||
				objectCount(
					t,
					fixture.client,
					fixture.measurement.Namespace,
					&edgev1alpha1.NodeActuationList{},
				) != 1 {
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
			fixture := newAttemptMaterializerFixture(t)
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
		source func(*testing.T) *edgev1alpha1.NodeActuation
		reason string
	}{
		{name: "missing", reason: AttemptReasonSourceStateUnknown},
		{name: "nonterminal", reason: AttemptReasonSourceStateUnknown, source: func(t *testing.T) *edgev1alpha1.NodeActuation {
			source := confirmedSourceActuation(t, 0, time.Unix(100, 0))
			source.Status = edgev1alpha1.NodeActuationStatus{}
			return source
		}},
		{name: "wrong power mode", reason: AttemptReasonSourceStateMismatch, source: func(t *testing.T) *edgev1alpha1.NodeActuation {
			source := confirmedSourceActuation(t, 0, time.Unix(100, 0))
			source.Status.TargetObservation.PowerMode = "other"
			return source
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var objects []client.Object
			if test.source != nil {
				objects = append(objects, test.source(t))
			}
			fixture := newAttemptMaterializerFixture(t, objects...)
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
	fixture := newAttemptMaterializerFixture(t, confirmedSourceActuation(t, 0, time.Unix(100, 0)))
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

func TestAttemptMaterializerRecoversConcurrentNodeActuation(t *testing.T) {
	fixture := newAttemptMaterializerFixture(t, confirmedSourceActuation(t, 0, time.Unix(100, 0)))
	progress, err := fixture.materializer.Advance(
		context.Background(), fixture.measurement, fixture.prepared, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	setJobReady(t, fixture.client, progress.Job)
	raceClient := &attemptCreationRaceClient{Client: fixture.client, target: raceNodeActuation}
	fixture.materializer.client = raceClient

	progress, err = fixture.materializer.Advance(
		context.Background(), fixture.measurement, fixture.prepared, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if progress.Stage != AttemptStageActuationRunning || progress.NodeActuation == nil ||
		progress.NodeActuation.UID != "concurrent-winner" ||
		!progress.NodeActuation.Spec.StageRequestedAt.Time.Equal(time.Unix(150, 0).UTC()) ||
		raceClient.createCalls != 1 || fixture.stores.calls != 1 {
		t.Fatalf(
			"concurrent NodeActuation progress = %#v, creates = %d, store calls = %d",
			progress, raceClient.createCalls, fixture.stores.calls,
		)
	}

	if _, err := fixture.materializer.Advance(
		context.Background(), fixture.measurement, fixture.prepared, 0, 1,
	); err != nil {
		t.Fatal(err)
	}
	if raceClient.createCalls != 1 || fixture.stores.calls != 1 {
		t.Fatalf(
			"retained NodeActuation was recreated: creates = %d, store calls = %d",
			raceClient.createCalls, fixture.stores.calls,
		)
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
		{
			name: "NodeActuation", target: raceNodeActuation,
			prepare: func(t *testing.T, fixture attemptMaterializerFixture) {
				progress, err := fixture.materializer.Advance(
					context.Background(), fixture.measurement, fixture.prepared, 0, 1,
				)
				if err != nil {
					t.Fatal(err)
				}
				setJobReady(t, fixture.client, progress.Job)
			},
			wantContext: "get transition attempt NodeActuation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, confirmedSourceActuation(t, 0, time.Unix(100, 0)))
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
		t.Run(string(conditionType), func(t *testing.T) {
			fixture := newAttemptMaterializerFixture(t, confirmedSourceActuation(t, 0, time.Unix(100, 0)))
			progress, err := fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			setJobReady(t, fixture.client, progress.Job)
			progress, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			ready := metav1.NewMicroTime(time.Unix(200, 0))
			previous := metav1.NewMicroTime(time.Unix(198, 0))
			target := metav1.NewMicroTime(time.Unix(199, 0))
			actuation := progress.NodeActuation.DeepCopy()
			actuation.Status = edgev1alpha1.NodeActuationStatus{
				ObservedGeneration: actuation.Generation,
				Phase:              edgev1alpha1.NodeActuationPhaseReady,
				ReadyAt:            &ready,
				PreviousObservation: &edgev1alpha1.NodeActuationObservation{
					PowerMode: fixture.prepared.Directions[0].FrozenContract.SourceState.PowerMode,
					Actuator:  *fixture.prepared.Directions[1].Actuator.DeepCopy(), ObservedAt: previous,
				},
				TargetObservation: &edgev1alpha1.NodeActuationObservation{
					PowerMode: actuation.Spec.PowerMode, Actuator: *actuation.Spec.Actuator.DeepCopy(), ObservedAt: target,
				},
			}
			if err := fixture.client.Status().Update(context.Background(), actuation); err != nil {
				t.Fatal(err)
			}
			setJobCondition(t, fixture.client, progress.Job, conditionType)

			progress, err = fixture.materializer.Advance(
				context.Background(), fixture.measurement, fixture.prepared, 0, 1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if progress.Stage != AttemptStageFinalizationReady || progress.Job == nil || progress.NodeActuation == nil ||
				progress.Job.Name != progress.NodeActuation.Name {
				t.Fatalf("finalization progress = %#v", progress)
			}
		})
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

const (
	raceObserverJob   attemptCreationRaceTarget = "observer-job"
	raceNodeActuation attemptCreationRaceTarget = "node-actuation"
)

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
	resource := schema.GroupResource{Group: "batch", Resource: "jobs"}
	if c.target == raceNodeActuation {
		resource = schema.GroupResource{Group: edgev1alpha1.GroupVersion.Group, Resource: "nodeactuations"}
	}
	return apierrors.NewAlreadyExists(resource, object.GetName())
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
	switch object.(type) {
	case *batchv1.Job:
		return c.target == raceObserverJob
	case *edgev1alpha1.NodeActuation:
		return c.target == raceNodeActuation
	default:
		return false
	}
}

func newAttemptMaterializerFixture(t *testing.T, objects ...client.Object) attemptMaterializerFixture {
	t.Helper()
	measurement, prepared, scheme := preparationRecordFixture(t)
	measurement.Spec.AttemptLimit = 3
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&batchv1.Job{}, &edgev1alpha1.NodeActuation{}).
		WithObjects(objects...).Build()
	stores := &recordingAttemptStoreResolver{binding: storekube.Binding{
		Store: attemptEvidenceStore, Endpoint: "http://store", Bucket: "bucket", Region: "region",
		CredentialsSecretName: "credentials",
	}}
	_, options, _ := validAttemptJobInputs(t)
	options.ActiveDeadlineOverhead = 20 * time.Minute
	materializer, err := NewAttemptMaterializer(kubeClient, scheme, stores, options)
	if err != nil {
		t.Fatal(err)
	}
	materializer.now = func() time.Time { return time.Unix(150, 0) }
	return attemptMaterializerFixture{
		measurement: measurement, prepared: prepared, client: kubeClient,
		materializer: materializer, stores: stores,
	}
}

func confirmedSourceActuation(t *testing.T, direction int, terminalAt time.Time) *edgev1alpha1.NodeActuation {
	t.Helper()
	_, prepared, _ := preparationRecordFixture(t)
	sourceState := prepared.Directions[direction].FrozenContract.SourceState
	sourceActuator := prepared.Directions[1-direction].Actuator
	observedAt := metav1.NewMicroTime(terminalAt.Add(-time.Second))
	readyAt := metav1.NewMicroTime(terminalAt)
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: prepared.Namespace, Name: "source-" + strings.ReplaceAll(sourceState.PowerMode, "_", "-"),
			Generation: 1,
		},
		Spec: edgev1alpha1.NodeActuationSpec{
			Node: prepared.TargetNode, DeviceClass: prepared.DeviceClass,
			PowerMode: sourceState.PowerMode, Actuator: *sourceActuator.DeepCopy(),
		},
		Status: edgev1alpha1.NodeActuationStatus{
			ObservedGeneration: 1, Phase: edgev1alpha1.NodeActuationPhaseReady, ReadyAt: &readyAt,
			TargetObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: sourceState.PowerMode, Actuator: *sourceActuator.DeepCopy(), ObservedAt: observedAt,
			},
		},
	}
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
	case *edgev1alpha1.NodeActuationList:
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
