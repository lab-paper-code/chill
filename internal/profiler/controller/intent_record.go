package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	intentRecordIntentKey          = "intent.json"
	intentRecordCandidateKey       = "candidate-report.json"
	intentRecordAdmissionPolicyKey = "admission-policy.json"
	intentDigestAnnotation         = "edge.dacs.io/profiling-run-intent-digest"
	admissionPolicyAnnotation      = "edge.dacs.io/profiling-run-admission-policy-digest"
	intentRecordNameHashCharacters = 32
)

// IntentRecords owns only the immutable frozen-input ConfigMap lifecycle.
// It never creates a Job or writes ProfilingRun status.
type IntentRecords struct {
	client client.Client
	scheme *runtime.Scheme
}

func NewIntentRecords(kubeClient client.Client, scheme *runtime.Scheme) (*IntentRecords, error) {
	if kubeClient == nil || scheme == nil {
		return nil, errors.New("Kubernetes client and scheme are required")
	}
	return &IntentRecords{client: kubeClient, scheme: scheme}, nil
}

// Ensure creates the deterministic record once or verifies and reuses the
// existing record. It never patches conflicting or malformed content.
func (r *IntentRecords) Ensure(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
) (*corev1.ConfigMap, bool, error) {
	desired, err := buildIntentRecord(run, frozen, r.scheme)
	if err != nil {
		return nil, false, err
	}
	key := client.ObjectKeyFromObject(desired)
	existing := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, key, existing); err == nil {
		if err := verifyExpectedIntentRecord(run, existing, frozen); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, false, fmt.Errorf("get frozen intent ConfigMap %s: %w", key, err)
	}

	if err := r.client.Create(ctx, desired); err == nil {
		return desired, true, nil
	} else if !apierrors.IsAlreadyExists(err) {
		return nil, false, fmt.Errorf("create frozen intent ConfigMap %s: %w", key, err)
	}
	if err := r.client.Get(ctx, key, existing); err != nil {
		return nil, false, fmt.Errorf("get concurrently created frozen intent ConfigMap %s: %w", key, err)
	}
	if err := verifyExpectedIntentRecord(run, existing, frozen); err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

func (r *IntentRecords) Load(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (FrozenRun, error) {
	name, err := intentRecordName(run)
	if err != nil {
		return FrozenRun{}, err
	}
	record := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: run.Namespace, Name: name}
	if err := r.client.Get(ctx, key, record); err != nil {
		return FrozenRun{}, fmt.Errorf("get frozen intent ConfigMap %s: %w", key, err)
	}
	return decodeIntentRecord(run, record)
}

func buildIntentRecord(
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
	scheme *runtime.Scheme,
) (*corev1.ConfigMap, error) {
	if run == nil || scheme == nil {
		return nil, errors.New("ProfilingRun and scheme are required")
	}
	name, err := intentRecordName(run)
	if err != nil {
		return nil, err
	}
	intentBytes, policyBytes, err := validateFrozenRunForRun(run, frozen)
	if err != nil {
		return nil, err
	}
	policyDigest, err := frozen.AdmissionPolicy.Policy.Digest()
	if err != nil {
		return nil, fmt.Errorf("digest frozen admission policy: %w", err)
	}
	record := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: run.Namespace,
			Name:      name,
			Labels: map[string]string{
				"app.kubernetes.io/name":      component.DefaultSystemName,
				"app.kubernetes.io/component": component.Profiler,
			},
			Annotations: map[string]string{
				intentDigestAnnotation:    frozen.Prepared.IntentDigest,
				admissionPolicyAnnotation: policyDigest,
			},
		},
		Immutable: ptr.To(true),
		BinaryData: map[string][]byte{
			intentRecordIntentKey:          append([]byte(nil), intentBytes...),
			intentRecordCandidateKey:       append([]byte(nil), frozen.Prepared.CandidateReportBytes...),
			intentRecordAdmissionPolicyKey: append([]byte(nil), policyBytes...),
		},
	}
	if err := controllerutil.SetControllerReference(run, record, scheme); err != nil {
		return nil, fmt.Errorf("set frozen intent ConfigMap owner: %w", err)
	}
	return record, nil
}

func validateFrozenRunForRun(
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
) ([]byte, []byte, error) {
	if err := frozen.Validate(); err != nil {
		return nil, nil, err
	}
	intentBytes, err := validatePreparedForRun(run, frozen.Prepared)
	if err != nil {
		return nil, nil, err
	}
	if frozen.AdmissionPolicy.Policy.Name != run.Spec.AdmissionPolicy {
		return nil, nil, errors.New("frozen admission policy does not match immutable ProfilingRun spec")
	}
	if frozen.AdmissionPolicy.Policy.WorkloadMode != frozen.Prepared.Intent.Measurement.WorkloadMode {
		return nil, nil, errors.New("frozen admission policy workload mode does not match immutable Run intent")
	}
	policyBytes, err := frozen.AdmissionPolicy.CanonicalJSON()
	if err != nil {
		return nil, nil, fmt.Errorf("encode frozen admission policy: %w", err)
	}
	return intentBytes, policyBytes, nil
}

func intentRecordName(run *edgev1alpha1.ProfilingRun) (string, error) {
	if run == nil || run.Namespace == "" || run.Name == "" || run.UID == "" {
		return "", errors.New("ProfilingRun namespace, name, and UID are required")
	}
	sum := sha256.Sum256([]byte(run.UID))
	hash := hex.EncodeToString(sum[:])[:intentRecordNameHashCharacters]
	return "profilingrun-" + hash + "-intent", nil
}

func validatePreparedForRun(
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
) ([]byte, error) {
	request, err := ProjectRequest(run.Spec)
	if err != nil {
		return nil, err
	}
	actualRequest := preparationFromIntent(prepared.Intent)
	if !reflect.DeepEqual(request.Preparation, actualRequest) {
		return nil, errors.New("prepared intent does not match immutable ProfilingRun spec")
	}
	intentBytes, err := prepared.Intent.CanonicalJSON()
	if err != nil {
		return nil, fmt.Errorf("encode prepared Run intent: %w", err)
	}
	digest, err := prepared.Intent.Digest()
	if err != nil {
		return nil, fmt.Errorf("digest prepared Run intent: %w", err)
	}
	if prepared.IntentDigest != digest {
		return nil, errors.New("prepared Run intent digest mismatch")
	}
	if err := profilingrun.ValidateCandidateReport(prepared.CandidateReportBytes, prepared.Intent); err != nil {
		return nil, fmt.Errorf("validate prepared candidate report: %w", err)
	}
	return intentBytes, nil
}

func preparationFromIntent(intent profilingrun.Intent) profiler.PreparationRequest {
	return profiler.PreparationRequest{
		Candidate: profiler.CandidateSelection{
			ModelSpec:     intent.State.Model,
			DeviceClass:   intent.State.DeviceClass,
			ExecutionPath: intent.Candidate.ExecutionPath,
			PowerMode:     intent.State.PowerMode,
		},
		TargetNode:  intent.TargetNode.Name,
		CPURequest:  intent.CPU.Request,
		Measurement: intent.Measurement,
		Metrics: profiler.MetricObservationPolicy{
			IntervalMilliseconds: intent.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  intent.Metrics.TimeoutMilliseconds,
			DurationSeconds:      intent.Metrics.DurationSeconds,
		},
	}
}

func verifyExpectedIntentRecord(
	run *edgev1alpha1.ProfilingRun,
	record *corev1.ConfigMap,
	expected FrozenRun,
) error {
	retained, err := decodeIntentRecord(run, record)
	if err != nil {
		return err
	}
	retainedPolicy, retainedPolicyErr := retained.AdmissionPolicy.CanonicalJSON()
	expectedPolicy, expectedPolicyErr := expected.AdmissionPolicy.CanonicalJSON()
	if retainedPolicyErr != nil || expectedPolicyErr != nil {
		return errors.Join(retainedPolicyErr, expectedPolicyErr)
	}
	if retained.Prepared.IntentDigest != expected.Prepared.IntentDigest ||
		!bytes.Equal(retained.Prepared.CandidateReportBytes, expected.Prepared.CandidateReportBytes) ||
		!bytes.Equal(retainedPolicy, expectedPolicy) {
		return errors.New("existing frozen intent ConfigMap contains different Run inputs")
	}
	return nil
}

func decodeIntentRecord(
	run *edgev1alpha1.ProfilingRun,
	record *corev1.ConfigMap,
) (FrozenRun, error) {
	if run == nil || record == nil {
		return FrozenRun{}, errors.New("ProfilingRun and frozen intent ConfigMap are required")
	}
	name, err := intentRecordName(run)
	if err != nil {
		return FrozenRun{}, err
	}
	if record.Namespace != run.Namespace || record.Name != name || !metav1.IsControlledBy(record, run) {
		return FrozenRun{}, errors.New("frozen intent ConfigMap identity or ownership mismatch")
	}
	if record.Immutable == nil || !*record.Immutable {
		return FrozenRun{}, errors.New("frozen intent ConfigMap is not immutable")
	}
	if len(record.Data) != 0 || (len(record.BinaryData) != 2 && len(record.BinaryData) != 3) {
		return FrozenRun{}, errors.New("frozen intent ConfigMap has unexpected data keys")
	}
	intentBytes, intentOK := record.BinaryData[intentRecordIntentKey]
	candidateBytes, candidateOK := record.BinaryData[intentRecordCandidateKey]
	if !intentOK || !candidateOK || len(intentBytes) == 0 || len(candidateBytes) == 0 {
		return FrozenRun{}, errors.New("frozen intent ConfigMap payload is incomplete")
	}

	intent, digest, err := decodeStoredIntent(intentBytes)
	if err != nil {
		return FrozenRun{}, err
	}
	if record.Annotations[intentDigestAnnotation] != digest {
		return FrozenRun{}, errors.New("frozen intent ConfigMap annotation digest mismatch")
	}
	prepared := profiler.PreparedRun{
		Intent: intent, IntentDigest: digest,
		CandidateReportBytes: append([]byte(nil), candidateBytes...),
	}
	policy, err := decodeStoredAdmissionPolicy(run, record, intent.Measurement.WorkloadMode)
	if err != nil {
		return FrozenRun{}, err
	}
	frozen := FrozenRun{Prepared: prepared, AdmissionPolicy: policy}
	if _, _, err := validateFrozenRunForRun(run, frozen); err != nil {
		return FrozenRun{}, err
	}
	return frozen, nil
}

func decodeStoredIntent(raw []byte) (profilingrun.Intent, string, error) {
	var intent profilingrun.Intent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return profilingrun.Intent{}, "", fmt.Errorf("decode frozen Run intent: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return profilingrun.Intent{}, "", errors.New("frozen Run intent contains a trailing JSON value")
	}
	digest, err := intent.Digest()
	if err != nil {
		return profilingrun.Intent{}, "", fmt.Errorf("validate frozen Run intent: %w", err)
	}
	canonical, err := intent.CanonicalJSON()
	if err != nil {
		return profilingrun.Intent{}, "", fmt.Errorf("encode validated frozen Run intent: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return profilingrun.Intent{}, "", errors.New("stored Run intent is not canonical")
	}
	return intent, digest, nil
}

func decodeStoredAdmissionPolicy(
	run *edgev1alpha1.ProfilingRun,
	record *corev1.ConfigMap,
	workloadMode string,
) (profileradmission.FrozenPolicy, error) {
	policyBytes, hasPolicy := record.BinaryData[intentRecordAdmissionPolicyKey]
	if !hasPolicy {
		if len(record.BinaryData) != 2 || record.Annotations[admissionPolicyAnnotation] != "" {
			return profileradmission.FrozenPolicy{}, errors.New("legacy frozen intent ConfigMap has inconsistent admission-policy data")
		}
		return profileradmission.LegacyFrozenPolicy(run.Spec.AdmissionPolicy, workloadMode)
	}
	if len(record.BinaryData) != 3 || len(policyBytes) == 0 {
		return profileradmission.FrozenPolicy{}, errors.New("frozen admission-policy payload is incomplete")
	}
	policy, err := profileradmission.DecodeFrozenPolicy(policyBytes)
	if err != nil {
		return profileradmission.FrozenPolicy{}, err
	}
	policyDigest, err := policy.Policy.Digest()
	if err != nil {
		return profileradmission.FrozenPolicy{}, err
	}
	if record.Annotations[admissionPolicyAnnotation] != policyDigest {
		return profileradmission.FrozenPolicy{}, errors.New("frozen admission-policy annotation digest mismatch")
	}
	return policy, nil
}
