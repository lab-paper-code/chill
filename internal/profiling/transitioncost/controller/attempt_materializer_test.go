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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			if fixture.stores.calls != 1 || objectCount(t, fixture.client, fixture.measurement.Namespace, &batchv1.JobList{}) != 1 ||
				objectCount(t, fixture.client, fixture.measurement.Namespace, &edgev1alpha1.NodeActuationList{}) != 2 {
				t.Fatalf("recovery duplicated attempt resources; store calls = %d", fixture.stores.calls)
			}
		})
	}
}

func requireFinalLocation(t *testing.T, measurement *edgev1alpha1.PowerModeTransitionCost,
	direction int, attempt int32, want string,
) {
	t.Helper()
	selection, err := selectAttempt(measurement, direction, attempt)
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
			job := progress.Job.DeepCopy()
			job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{
				Type: conditionType, Status: corev1.ConditionTrue,
			})
			if err := fixture.client.Status().Update(context.Background(), job); err != nil {
				t.Fatal(err)
			}

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

func newAttemptMaterializerFixture(t *testing.T, objects ...client.Object) attemptMaterializerFixture {
	t.Helper()
	measurement, prepared, scheme := preparationRecordFixture(t)
	measurement.Spec.AttemptLimit = 3
	measurement.Spec.EvidenceStore = attemptEvidenceStore
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
	options.ActiveDeadline = 20 * time.Minute
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
