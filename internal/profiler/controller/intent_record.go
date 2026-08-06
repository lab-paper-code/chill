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
	intentRecordIntentKey           = "intent.json"
	intentRecordCandidateKey        = "candidate-report.json"
	intentRecordAdmissionPolicyKey  = "admission-policy.json"
	intentRecordExecutionPolicyKey  = "execution-policy.json"
	intentRecordActuationPolicyKey  = "actuation-policy.json"
	intentDigestAnnotation          = "edge.dacs.io/profiling-run-intent-digest"
	admissionPolicyAnnotation       = "edge.dacs.io/profiling-run-admission-policy-digest"
	executionPolicyRecordAnnotation = "edge.dacs.io/profiling-run-execution-policy-digest"
	actuationPolicyRecordAnnotation = "edge.dacs.io/profiling-run-actuation-policy-digest"
	intentRecordNameHashCharacters  = 32
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
	validated, err := validateFrozenRunForRun(run, frozen)
	if err != nil {
		return nil, err
	}
	policyDigest, err := frozen.AdmissionPolicy.Digest()
	if err != nil {
		return nil, fmt.Errorf("digest frozen admission policy: %w", err)
	}
	executionPolicyDigest, err := frozen.ExecutionPolicy.Policy.Digest()
	if err != nil {
		return nil, fmt.Errorf("digest frozen execution policy: %w", err)
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
				intentDigestAnnotation:          frozen.Prepared.IntentDigest,
				admissionPolicyAnnotation:       policyDigest,
				executionPolicyRecordAnnotation: executionPolicyDigest,
			},
		},
		Immutable: ptr.To(true),
		BinaryData: map[string][]byte{
			intentRecordIntentKey:          append([]byte(nil), validated.IntentBytes...),
			intentRecordCandidateKey:       append([]byte(nil), frozen.Prepared.CandidateReportBytes...),
			intentRecordAdmissionPolicyKey: append([]byte(nil), validated.AdmissionPolicyBytes...),
			intentRecordExecutionPolicyKey: append([]byte(nil), validated.ExecutionPolicyBytes...),
		},
	}
	if frozen.Prepared.ActuationPolicy != nil {
		actuationPolicyDigest, err := frozen.Prepared.ActuationPolicy.Policy.Digest()
		if err != nil {
			return nil, fmt.Errorf("digest frozen actuation policy: %w", err)
		}
		record.Annotations[actuationPolicyRecordAnnotation] = actuationPolicyDigest
		record.BinaryData[intentRecordActuationPolicyKey] = append(
			[]byte(nil),
			validated.ActuationPolicyBytes...,
		)
	}
	if err := controllerutil.SetControllerReference(run, record, scheme); err != nil {
		return nil, fmt.Errorf("set frozen intent ConfigMap owner: %w", err)
	}
	return record, nil
}

type validatedFrozenRunMaterial struct {
	IntentBytes          []byte
	AdmissionPolicyBytes []byte
	ExecutionPolicyBytes []byte
	ActuationPolicyBytes []byte
	CandidateReport      profilingrun.VerifiedCandidateReport
}

func validateFrozenRunForRun(
	run *edgev1alpha1.ProfilingRun,
	frozen FrozenRun,
) (validatedFrozenRunMaterial, error) {
	if err := frozen.Validate(); err != nil {
		return validatedFrozenRunMaterial{}, err
	}
	intentBytes, candidateReport, err := validatePreparedForRun(run, frozen.Prepared)
	if err != nil {
		return validatedFrozenRunMaterial{}, err
	}
	if err := validateSelectedPolicies(run, frozen); err != nil {
		return validatedFrozenRunMaterial{}, err
	}
	admissionPolicyBytes, err := frozen.AdmissionPolicy.CanonicalJSON()
	if err != nil {
		return validatedFrozenRunMaterial{}, fmt.Errorf("encode frozen admission policy: %w", err)
	}
	executionPolicyBytes, err := frozen.ExecutionPolicy.CanonicalJSON()
	if err != nil {
		return validatedFrozenRunMaterial{}, fmt.Errorf("encode frozen execution policy: %w", err)
	}
	var actuationPolicyBytes []byte
	if frozen.Prepared.ActuationPolicy != nil {
		actuationPolicyBytes, err = frozen.Prepared.ActuationPolicy.CanonicalJSON()
		if err != nil {
			return validatedFrozenRunMaterial{}, fmt.Errorf("encode frozen actuation policy: %w", err)
		}
	}
	return validatedFrozenRunMaterial{
		IntentBytes: intentBytes, AdmissionPolicyBytes: admissionPolicyBytes,
		ExecutionPolicyBytes: executionPolicyBytes, ActuationPolicyBytes: actuationPolicyBytes,
		CandidateReport: candidateReport,
	}, nil
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
) ([]byte, profilingrun.VerifiedCandidateReport, error) {
	request, err := ProjectRequest(run.Spec)
	if err != nil {
		return nil, profilingrun.VerifiedCandidateReport{}, err
	}
	actualRequest := preparationFromIntent(prepared.Intent)
	if !reflect.DeepEqual(request.Preparation, actualRequest) {
		return nil, profilingrun.VerifiedCandidateReport{}, errors.New("prepared intent does not match immutable ProfilingRun spec")
	}
	intentBytes, err := prepared.Intent.CanonicalJSON()
	if err != nil {
		return nil, profilingrun.VerifiedCandidateReport{}, fmt.Errorf("encode prepared Run intent: %w", err)
	}
	verified, err := profilingrun.VerifyCandidateReport(prepared.CandidateReportBytes, prepared.Intent)
	if err != nil {
		return nil, profilingrun.VerifiedCandidateReport{}, fmt.Errorf("validate prepared candidate report: %w", err)
	}
	return intentBytes, verified, nil
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
	retainedExecution, retainedExecutionErr := retained.ExecutionPolicy.CanonicalJSON()
	expectedExecution, expectedExecutionErr := expected.ExecutionPolicy.CanonicalJSON()
	retainedActuation, retainedActuationErr := canonicalActuationPolicy(retained.Prepared.ActuationPolicy)
	expectedActuation, expectedActuationErr := canonicalActuationPolicy(expected.Prepared.ActuationPolicy)
	if retainedPolicyErr != nil || expectedPolicyErr != nil ||
		retainedExecutionErr != nil || expectedExecutionErr != nil ||
		retainedActuationErr != nil || expectedActuationErr != nil {
		return errors.Join(
			retainedPolicyErr,
			expectedPolicyErr,
			retainedExecutionErr,
			expectedExecutionErr,
			retainedActuationErr,
			expectedActuationErr,
		)
	}
	if retained.Prepared.IntentDigest != expected.Prepared.IntentDigest ||
		!bytes.Equal(retained.Prepared.CandidateReportBytes, expected.Prepared.CandidateReportBytes) ||
		!bytes.Equal(retainedPolicy, expectedPolicy) ||
		!bytes.Equal(retainedExecution, expectedExecution) ||
		!bytes.Equal(retainedActuation, expectedActuation) {
		return errors.New("existing frozen intent ConfigMap contains different Run inputs")
	}
	return nil
}

func canonicalActuationPolicy(policy *profiler.FrozenActuationPolicy) ([]byte, error) {
	if policy == nil {
		return nil, nil
	}
	return policy.CanonicalJSON()
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
	if len(record.Data) != 0 {
		return FrozenRun{}, errors.New("frozen intent ConfigMap has unexpected text data")
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
	expectedDataKeys := 4
	if intent.Actuation != nil {
		expectedDataKeys++
	}
	if len(record.BinaryData) != expectedDataKeys {
		return FrozenRun{}, errors.New("frozen intent ConfigMap has unexpected data keys")
	}
	prepared := profiler.PreparedRun{
		Intent: intent, IntentDigest: digest,
		CandidateReportBytes: append([]byte(nil), candidateBytes...),
	}
	actuationPolicy, err := decodeStoredActuationPolicy(record, intent.Actuation != nil)
	if err != nil {
		return FrozenRun{}, err
	}
	prepared.ActuationPolicy = actuationPolicy
	policy, err := decodeStoredAdmissionPolicy(record)
	if err != nil {
		return FrozenRun{}, err
	}
	executionPolicy, err := decodeStoredExecutionPolicy(record)
	if err != nil {
		return FrozenRun{}, err
	}
	frozen := FrozenRun{
		Prepared:        prepared,
		AdmissionPolicy: policy,
		ExecutionPolicy: executionPolicy,
	}
	if _, err := validateFrozenRunForRun(run, frozen); err != nil {
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

func decodeStoredAdmissionPolicy(record *corev1.ConfigMap) (profileradmission.FrozenPolicy, error) {
	policyBytes := record.BinaryData[intentRecordAdmissionPolicyKey]
	if len(policyBytes) == 0 {
		return profileradmission.FrozenPolicy{}, errors.New("frozen admission-policy payload is incomplete")
	}
	policy, err := profileradmission.DecodeFrozenPolicy(policyBytes)
	if err != nil {
		return profileradmission.FrozenPolicy{}, err
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return profileradmission.FrozenPolicy{}, err
	}
	if record.Annotations[admissionPolicyAnnotation] != policyDigest {
		return profileradmission.FrozenPolicy{}, errors.New("frozen admission-policy annotation digest mismatch")
	}
	return policy, nil
}

func decodeStoredExecutionPolicy(record *corev1.ConfigMap) (profiler.FrozenExecutionPolicy, error) {
	policyBytes := record.BinaryData[intentRecordExecutionPolicyKey]
	if len(policyBytes) == 0 {
		return profiler.FrozenExecutionPolicy{}, errors.New("frozen execution-policy payload is incomplete")
	}
	policy, err := profiler.DecodeFrozenExecutionPolicy(policyBytes)
	if err != nil {
		return profiler.FrozenExecutionPolicy{}, err
	}
	policyDigest, err := policy.Policy.Digest()
	if err != nil {
		return profiler.FrozenExecutionPolicy{}, err
	}
	if record.Annotations[executionPolicyRecordAnnotation] != policyDigest {
		return profiler.FrozenExecutionPolicy{}, errors.New("frozen execution-policy annotation digest mismatch")
	}
	return policy, nil
}

func decodeStoredActuationPolicy(
	record *corev1.ConfigMap,
	required bool,
) (*profiler.FrozenActuationPolicy, error) {
	policyBytes, present := record.BinaryData[intentRecordActuationPolicyKey]
	annotation, annotated := record.Annotations[actuationPolicyRecordAnnotation]
	if !required {
		if present || annotated {
			return nil, errors.New("fixed Run record cannot contain an actuation policy")
		}
		return nil, nil
	}
	if !present || len(policyBytes) == 0 || !annotated {
		return nil, errors.New("frozen actuation-policy payload is incomplete")
	}
	policy, err := profiler.DecodeFrozenActuationPolicy(policyBytes)
	if err != nil {
		return nil, err
	}
	policyDigest, err := policy.Policy.Digest()
	if err != nil {
		return nil, err
	}
	if annotation != policyDigest {
		return nil, errors.New("frozen actuation-policy annotation digest mismatch")
	}
	return &policy, nil
}
