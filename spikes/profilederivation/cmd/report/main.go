package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/powerobserver"
	"github.com/lab-paper-code/chill/internal/profilederivation"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type evidence struct {
	SchemaVersion   string                     `json:"schemaVersion"`
	CollectedAt     string                     `json:"collectedAt"`
	IntentDigest    string                     `json:"intentDigest"`
	CandidateReport json.RawMessage            `json:"candidateReport"`
	Intent          profilingrun.Intent        `json:"intent"`
	Job             batchv1.Job                `json:"job"`
	Pod             corev1.Pod                 `json:"pod"`
	ActualNode      corev1.Node                `json:"actualNode"`
	CoResidentPods  corev1.PodList             `json:"coResidentPods"`
	Runtime         profilingrun.RuntimeResult `json:"runtime"`
	Power           powerobserver.Result       `json:"power"`
}

type report struct {
	SchemaVersion     string                            `json:"schemaVersion"`
	EvidenceDigest    string                            `json:"evidenceDigest"`
	Admission         profilederivation.AdmissionResult `json:"admission"`
	IncrementalEnergy struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	} `json:"incrementalEnergy"`
	BSat profilederivation.BSatResult `json:"bSat"`
}

func main() {
	evidencePath := flag.String("evidence", "", "Plan 2 evidence bundle")
	policyPath := flag.String("policy", "", "admission policy JSON")
	flag.Parse()
	if *evidencePath == "" || *policyPath == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "-evidence and -policy are required")
		os.Exit(2)
	}
	payload, err := os.ReadFile(*evidencePath)
	if err != nil {
		fatal(err)
	}
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	expected := "sha256-" + hex.EncodeToString(sum[:]) + ".json"
	if filepath.Base(*evidencePath) != expected {
		fatal(errors.New("evidence filename does not match exact content digest"))
	}
	policyPayload, err := os.ReadFile(*policyPath)
	if err != nil {
		fatal(err)
	}
	var policy profilederivation.AdmissionPolicy
	if err := strict(policyPayload, &policy); err != nil {
		fatal(err)
	}
	result, err := deriveReport(payload, digest, policy)
	if err != nil {
		fatal(err)
	}
	result.IncrementalEnergy.Status = "Unavailable"
	result.IncrementalEnergy.Code = "MissingIdleBaseline"
	encoded, err := json.Marshal(result)
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(encoded))
}

func deriveReport(payload []byte, digest string, policy profilederivation.AdmissionPolicy) (report, error) {
	var e evidence
	if err := strict(payload, &e); err != nil {
		return report{}, err
	}
	if e.SchemaVersion != "chill.dacs.io/profiling-run-evidence.v1alpha1" {
		return report{}, errors.New("unsupported Plan 2 evidence schema")
	}
	if e.IntentDigest != mustIntentDigest(e.Intent) {
		return report{}, errors.New("intent digest mismatch")
	}
	if err := validatePlan2(e); err != nil {
		return report{}, err
	}
	latencies := []float64{}
	for _, repetition := range e.Runtime.RepetitionResults {
		latencies = append(latencies, repetition.LatencyMilliseconds...)
	}
	unexpected := []string{}
	for _, pod := range e.CoResidentPods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.UID != e.Pod.UID {
			unexpected = append(unexpected, pod.Namespace+"/"+pod.Name)
		}
	}
	stateIdentity := materialStateIdentity(e.Intent, string(e.ActualNode.UID))
	start, err := time.Parse(time.RFC3339Nano, e.Runtime.MeasurementStartedAt)
	if err != nil {
		return report{}, err
	}
	end, err := time.Parse(time.RFC3339Nano, e.Runtime.MeasurementEndedAt)
	if err != nil {
		return report{}, err
	}
	samples := make([]profilederivation.Sample, len(e.Power.Samples))
	for index, sample := range e.Power.Samples {
		samples[index] = profilederivation.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	trial := profilederivation.Trial{
		StateIdentity:         stateIdentity,
		TrialID:               string(e.Pod.UID),
		Batch:                 e.Intent.Measurement.Batch,
		WindowStart:           start,
		WindowEnd:             end,
		CompletedCalls:        e.Runtime.InferenceCount,
		CompletedItems:        e.Runtime.InferenceCount * e.Intent.Measurement.Batch,
		LatencyMilliseconds:   latencies,
		Samples:               samples,
		Failures:              len(e.Power.Failures),
		SourceTimestampAbsent: e.Power.Summary.SourceTimestampAbsent,
		UnexpectedCoResidents: unexpected,
		SteadyWindowVerified:  true,
		ThermalStateKnown:     false,
	}
	admission := profilederivation.Admit(trial, policy)
	result := report{
		SchemaVersion:  "spikes.chill.dacs.io/profile-derivation-report.v1alpha1",
		EvidenceDigest: digest,
		Admission:      admission,
		BSat: profilederivation.BSatResult{
			Status:          profilederivation.BSatUnavailable,
			PolicyVersion:   "not-applied",
			StateIdentity:   stateIdentity,
			Code:            "MissingIdleBaselineAndRepeatedCurve",
			MeasuredBatches: []int{trial.Batch},
		},
	}
	result.IncrementalEnergy.Status = "Unavailable"
	result.IncrementalEnergy.Code = "MissingIdleBaseline"
	return result, nil
}

func materialStateIdentity(intent profilingrun.Intent, nodeUID string) string {
	measurement := intent.Measurement
	measurement.Batch = 0
	payload, _ := json.Marshal(struct {
		State       profilingrun.ExecutionState      `json:"state"`
		Images      profilingrun.Images              `json:"images"`
		NodeUID     string                           `json:"nodeUID"`
		CPU         profilingrun.CPUContract         `json:"cpu"`
		Measurement profilingrun.MeasurementContract `json:"measurement"`
		Power       profilingrun.PowerContract       `json:"power"`
	}{intent.State, intent.Images, nodeUID, intent.CPU, measurement, intent.Power})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validatePlan2(e evidence) error {
	if err := e.Intent.Validate(); err != nil {
		return err
	}
	if e.Job.Status.Succeeded != 1 || e.Job.Status.Failed != 0 {
		return errors.New("Plan 2 Job outcome is not valid")
	}
	owned := false
	for _, owner := range e.Pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.UID == e.Job.UID {
			owned = true
		}
	}
	if !owned || e.Pod.Spec.NodeName != e.Intent.TargetNode.Name ||
		e.ActualNode.UID != types.UID(e.Intent.TargetNode.UID) {
		return errors.New("Plan 2 Kubernetes identity is invalid")
	}
	if err := profilingrun.ValidateRuntime(e.Intent, string(e.Pod.UID), e.Runtime); err != nil {
		return err
	}
	return nil
}
func mustIntentDigest(intent profilingrun.Intent) string {
	value, err := intent.Digest()
	if err != nil {
		return ""
	}
	return value
}

func strict(payload []byte, target any) error {
	if err := rejectDuplicateKeys(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func rejectDuplicateKeys(payload []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	if err := walkJSON(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}
func walkJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
