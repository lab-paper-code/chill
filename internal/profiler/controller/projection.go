// Package controller adapts Kubernetes API observations to the Kubernetes-free
// Profiler application contracts.
package controller

import (
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiler"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RequestedRun struct {
	Preparation     profiler.PreparationRequest
	AdmissionPolicy string
}

// ProjectRequest converts desired API state without resolving any cluster or
// endpoint fact.
func ProjectRequest(spec edgev1alpha1.ProfilingRunSpec) (RequestedRun, error) {
	request := RequestedRun{
		Preparation: profiler.PreparationRequest{
			Candidate: profiler.CandidateSelection{
				ModelSpec:     spec.Candidate.ModelSpec,
				DeviceClass:   spec.Candidate.DeviceClass,
				ExecutionPath: spec.Candidate.ExecutionPath,
				PowerMode:     spec.Candidate.PowerMode,
			},
			TargetNode: spec.TargetNode,
			CPURequest: spec.CPURequest.String(),
			Measurement: profilingrun.MeasurementContract{
				Batch:            int(spec.Measurement.Batch),
				WarmupIterations: int(spec.Measurement.WarmupIterations),
				DurationSeconds:  int(spec.Measurement.DurationSeconds),
				Repetitions:      int(spec.Measurement.Repetitions),
				WorkloadMode:     spec.Measurement.WorkloadMode,
				InputGenerator:   spec.Measurement.InputGenerator,
				OutputValidation: spec.Measurement.OutputValidation,
			},
			Metrics: profiler.MetricObservationPolicy{
				IntervalMilliseconds: int(spec.MetricObservation.IntervalMilliseconds),
				TimeoutMilliseconds:  int(spec.MetricObservation.TimeoutMilliseconds),
				DurationSeconds:      int(spec.MetricObservation.DurationSeconds),
			},
		},
		AdmissionPolicy: spec.AdmissionPolicy,
	}
	if spec.Measurement.SettlingDurationSeconds != nil {
		request.Preparation.Measurement.SettlingDurationSeconds = int(*spec.Measurement.SettlingDurationSeconds)
	}
	if spec.CPURequest.Sign() <= 0 {
		return RequestedRun{}, errors.New("ProfilingRun CPU request must be positive")
	}
	if strings.TrimSpace(request.AdmissionPolicy) == "" ||
		strings.TrimSpace(request.AdmissionPolicy) != request.AdmissionPolicy {
		return RequestedRun{}, errors.New("ProfilingRun admission policy is required without surrounding whitespace")
	}
	if err := request.Preparation.Validate(); err != nil {
		return RequestedRun{}, fmt.Errorf("validate ProfilingRun preparation request: %w", err)
	}
	return request, nil
}

// ProjectSnapshot reconstructs application lifecycle facts. When status has a
// Job reference, a nil job means the API lookup returned NotFound.
func ProjectSnapshot(
	run *edgev1alpha1.ProfilingRun,
	frozenIntent *profilingrun.Intent,
	job *batchv1.Job,
) (profiler.Snapshot, error) {
	if run == nil {
		return profiler.Snapshot{}, errors.New("ProfilingRun is required")
	}
	if !run.DeletionTimestamp.IsZero() {
		return profiler.Snapshot{Deleting: true}, nil
	}

	snapshot := profiler.Snapshot{}
	if err := projectIntent(run.Status.IntentDigest, frozenIntent, &snapshot); err != nil {
		return profiler.Snapshot{}, err
	}
	if err := projectJob(run, job, &snapshot); err != nil {
		return profiler.Snapshot{}, err
	}
	if err := projectEvidence(run.Status.Evidence, &snapshot); err != nil {
		return profiler.Snapshot{}, err
	}
	snapshot.AdmissionRecorded = run.Status.Admission != nil
	if err := snapshot.Validate(); err != nil {
		return profiler.Snapshot{}, fmt.Errorf("validate projected lifecycle snapshot: %w", err)
	}
	return snapshot, nil
}

func projectIntent(digest string, frozen *profilingrun.Intent, snapshot *profiler.Snapshot) error {
	if digest == "" && frozen == nil {
		return nil
	}
	if digest == "" || frozen == nil {
		return errors.New("intent digest and frozen intent must be observed together")
	}
	actual, err := frozen.Digest()
	if err != nil {
		return fmt.Errorf("validate observed frozen intent: %w", err)
	}
	if digest != actual {
		return errors.New("status intent digest does not match observed frozen intent")
	}
	copy := *frozen
	snapshot.Intent = &copy
	return nil
}

func projectJob(run *edgev1alpha1.ProfilingRun, job *batchv1.Job, snapshot *profiler.Snapshot) error {
	reference := run.Status.Job
	if reference == nil {
		if job != nil {
			return errors.New("Job was observed before its reference was recorded")
		}
		return nil
	}
	if strings.TrimSpace(reference.Name) == "" || strings.TrimSpace(reference.UID) == "" {
		return errors.New("recorded Job reference is incomplete")
	}
	snapshot.JobReference = reference.Name + "\x00" + reference.UID
	if job == nil {
		snapshot.Job = profiler.JobMissing
		return nil
	}
	if job.Namespace != run.Namespace || job.Name != reference.Name || string(job.UID) != reference.UID {
		return errors.New("observed Job identity does not match recorded reference")
	}
	if !metav1.IsControlledBy(job, run) {
		return errors.New("observed Job is not controlled by ProfilingRun")
	}
	observation, err := observeJob(job)
	if err != nil {
		return err
	}
	snapshot.Job = observation
	return nil
}

func observeJob(job *batchv1.Job) (profiler.JobObservation, error) {
	complete := false
	failed := false
	for _, condition := range job.Status.Conditions {
		complete = complete || condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue
		failed = failed || condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue
	}
	if complete && failed {
		return profiler.JobNotObserved, errors.New("Job reports both Complete and Failed")
	}
	if complete {
		if job.Status.Succeeded != 1 || job.Status.Failed != 0 {
			return profiler.JobNotObserved, errors.New("completed Job has ambiguous success counters")
		}
		return profiler.JobSucceeded, nil
	}
	if failed || job.Status.Failed > 0 {
		return profiler.JobFailed, nil
	}
	return profiler.JobActive, nil
}

func projectEvidence(reference *edgev1alpha1.ProfilingRunEvidenceReference, snapshot *profiler.Snapshot) error {
	if reference == nil {
		return nil
	}
	if reference.SchemaVersion != profilerevidence.ExecutionEvidenceSchemaVersion {
		return fmt.Errorf("unsupported execution evidence schemaVersion %q", reference.SchemaVersion)
	}
	identity := evidencestore.ObjectIdentity{
		Store: reference.Store, Key: reference.Key, Digest: reference.Digest, Size: reference.Size,
	}
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("validate status evidence reference: %w", err)
	}
	snapshot.EvidenceReference = &identity
	return nil
}
