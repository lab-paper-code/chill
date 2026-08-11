package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ResultStore provides immutable creation and restart recovery for one
// terminal batch-saturation result.
type ResultStore interface {
	evidencestore.ImmutableWriter
	evidencestore.LocationReader
}

// ResultWriterOptions configures immutable result storage and bounded recovery.
type ResultWriterOptions struct {
	Store          string
	MaxObjectBytes int64
}

// ResultWriter projects owned Runs into one immutable terminal result record.
type ResultWriter struct {
	store   ResultStore
	options ResultWriterOptions
}

func NewResultWriter(store ResultStore, options ResultWriterOptions) (*ResultWriter, error) {
	if store == nil {
		return nil, errors.New("batch-saturation result store is required")
	}
	if strings.TrimSpace(options.Store) == "" || strings.TrimSpace(options.Store) != options.Store {
		return nil, errors.New("result logical store is required without surrounding whitespace")
	}
	if options.MaxObjectBytes < 1 {
		return nil, errors.New("maximum result object size must be positive")
	}
	return &ResultWriter{store: store, options: options}, nil
}

// Write records one terminal result. Existing content remains authoritative
// and is returned only after semantic agreement with the recomputed record.
func (w *ResultWriter) Write(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
	curvePlan batchsaturation.StoredObjectReference,
	runs []edgev1alpha1.ProfilingRun,
	result batchsaturation.Result,
) (batchsaturation.StoredObjectReference, error) {
	if parent == nil {
		return batchsaturation.StoredObjectReference{}, errors.New("a parent BatchSaturation is required")
	}
	if parent.UID == "" {
		return batchsaturation.StoredObjectReference{}, errors.New("BatchSaturation UID is required")
	}

	detail, err := json.Marshal(result.Detail)
	if err != nil {
		return batchsaturation.StoredObjectReference{}, fmt.Errorf("encode result detail: %w", err)
	}
	admitted, excluded, err := projectResultTrials(parent, runs)
	if err != nil {
		return batchsaturation.StoredObjectReference{}, err
	}
	record, err := batchsaturation.NewResultRecord(batchsaturation.ResultRecordSpec{
		Outcome:        batchsaturation.TerminalOutcome(result.Outcome),
		CurvePlan:      curvePlan,
		AdmittedTrials: admitted,
		ExcludedTrials: excluded,
		Detail:         detail,
	})
	if err != nil {
		return batchsaturation.StoredObjectReference{}, fmt.Errorf("construct result record: %w", err)
	}
	return w.record(ctx, parent.UID, record)
}

func projectResultTrials(
	parent *edgev1alpha1.BatchSaturation,
	runs []edgev1alpha1.ProfilingRun,
) ([]batchsaturation.AdmittedTrialReference, []batchsaturation.ExcludedTrialReference, error) {
	admitted := make([]batchsaturation.AdmittedTrialReference, 0, len(runs))
	excluded := make([]batchsaturation.ExcludedTrialReference, 0, len(runs))
	for index := range runs {
		run := &runs[index]
		trial, err := projectOwnedTrial(parent, run)
		if err != nil {
			return nil, nil, fmt.Errorf("project ProfilingRun %q: %w", run.Name, err)
		}
		reference := batchsaturation.RunReference{Name: run.Name, UID: string(run.UID)}
		measurement := batchsaturation.ResultMeasurement{
			WorkloadMode: trial.Measurement.WorkloadMode,
			Batch:        trial.Measurement.Batch,
		}
		if trial.Verdict == batchsaturation.VerdictAccepted {
			if run.Status.Evidence == nil {
				return nil, nil, fmt.Errorf("admitted ProfilingRun %q has no evidence reference", run.Name)
			}
			admitted = append(admitted, batchsaturation.AdmittedTrialReference{
				Run:         reference,
				Measurement: measurement,
				Evidence:    projectStoredObjectReference(*run.Status.Evidence),
			})
			continue
		}
		reasons, err := exclusionReasons(run, trial.Verdict)
		if err != nil {
			return nil, nil, err
		}
		excluded = append(excluded, batchsaturation.ExcludedTrialReference{
			Run: reference, Measurement: measurement, Attempt: trial.Attempt, Reasons: reasons,
		})
	}
	return admitted, excluded, nil
}

func exclusionReasons(run *edgev1alpha1.ProfilingRun, verdict batchsaturation.Verdict) ([]string, error) {
	if run.Status.Admission != nil {
		reasons := append([]string(nil), run.Status.Admission.ReasonCodes...)
		if len(reasons) == 0 {
			reasons = []string{"Admission" + run.Status.Admission.Verdict}
		}
		return reasons, nil
	}
	if verdict == batchsaturation.VerdictPending {
		return []string{"Pending"}, nil
	}
	var reasons []string
	for _, condition := range run.Status.Conditions {
		if condition.Type != "IntentFrozen" && condition.Type != "ExecutionComplete" {
			continue
		}
		if condition.Status == metav1.ConditionFalse && isTerminalReason(condition.Reason) {
			reasons = append(reasons, condition.Reason)
		}
	}
	if len(reasons) == 0 {
		return nil, fmt.Errorf("excluded ProfilingRun %q has no exclusion reason", run.Name)
	}
	return reasons, nil
}

func projectStoredObjectReference(
	reference edgev1alpha1.ProfilingRunEvidenceReference,
) batchsaturation.StoredObjectReference {
	return batchsaturation.StoredObjectReference{
		Store: reference.Store, Key: reference.Key,
		SchemaVersion: reference.SchemaVersion, Size: reference.Size,
	}
}

func (w *ResultWriter) record(
	ctx context.Context,
	parentUID types.UID,
	record batchsaturation.ResultRecord,
) (batchsaturation.StoredObjectReference, error) {
	payload, err := record.CanonicalJSON()
	if err != nil {
		return batchsaturation.StoredObjectReference{}, fmt.Errorf("encode batch-saturation result: %w", err)
	}
	identity := evidencestore.ObjectIdentity{
		Store: w.options.Store,
		Key:   fmt.Sprintf("batch-saturations/%s/result.json", parentUID),
		Size:  int64(len(payload)),
	}
	reference := batchsaturation.StoredObjectReference{
		Store: identity.Store, Key: identity.Key,
		SchemaVersion: record.SchemaVersion(), Size: identity.Size,
	}
	receipt, err := w.store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return batchsaturation.StoredObjectReference{}, fmt.Errorf("record batch-saturation result: %w", err)
	}
	if err := receipt.Validate(); err != nil || receipt.Object != identity {
		return batchsaturation.StoredObjectReference{}, errors.New("result writer returned an invalid receipt")
	}
	if receipt.Outcome == evidencestore.CreateOutcomeCreated {
		return reference, nil
	}

	storedBytes, err := readStoredObject(
		ctx, w.store, identity.Location(), w.options.MaxObjectBytes, "batch-saturation result object",
	)
	if err != nil {
		return batchsaturation.StoredObjectReference{}, fmt.Errorf("read existing batch-saturation result: %w", err)
	}
	stored, err := batchsaturation.DecodeResultRecord(storedBytes)
	if err != nil {
		return batchsaturation.StoredObjectReference{}, fmt.Errorf("decode existing batch-saturation result: %w", err)
	}
	if !stored.SemanticallyEqual(record) {
		return batchsaturation.StoredObjectReference{}, errors.New(
			"stored batch-saturation result disagrees with the recomputed result",
		)
	}
	return batchsaturation.StoredObjectReference{
		Store: identity.Store, Key: identity.Key,
		SchemaVersion: stored.SchemaVersion(), Size: int64(len(storedBytes)),
	}, nil
}
