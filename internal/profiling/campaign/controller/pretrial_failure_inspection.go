package controller

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aws/smithy-go"
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PretrialFailureInspection records observations only. It grants no mutation authority.
type PretrialFailureInspection struct {
	APIVersion            string                      `json:"apiVersion"`
	Eligible              bool                        `json:"eligible"`
	Request               PretrialInspectionRequest   `json:"request"`
	Reason                string                      `json:"reason"`
	OwnedRunCount         int                         `json:"ownedRunCount"`
	PlanNotFound          bool                        `json:"planNotFound"`
	PublishedResultAbsent bool                        `json:"publishedResultAbsent"`
	Admission             PretrialInspectionAdmission `json:"admission"`
	Node                  PretrialInspectionNode      `json:"node"`
	CheckedAt             time.Time                   `json:"checkedAt"`
}

type PretrialInspectionRequest struct {
	Namespace       string    `json:"namespace"`
	Name            string    `json:"name"`
	UID             types.UID `json:"uid"`
	ResourceVersion string    `json:"resourceVersion"`
	Generation      int64     `json:"generation"`
}

type PretrialInspectionAdmission struct {
	Granted bool      `json:"granted"`
	At      time.Time `json:"at"`
}

type PretrialInspectionNode struct {
	Name            string    `json:"name"`
	UID             types.UID `json:"uid"`
	ResourceVersion string    `json:"resourceVersion"`
	Holder          string    `json:"holder"`
}

// InspectPretrialFailure uses fresh reads to identify only an unstarted request
// whose legacy campaign label is invalid. Errors deliberately contain no backend
// details: this proof is safe to emit without exposing credentials or endpoints.
func InspectPretrialFailure(ctx context.Context, reader client.Reader, objects evidencestore.LocationReader, logicalStore string, key types.NamespacedName, uid types.UID) (PretrialFailureInspection, error) {
	proof := PretrialFailureInspection{
		APIVersion: "chill.dacs.io/pretrial-retirement.v1",
		Request:    PretrialInspectionRequest{Namespace: key.Namespace, Name: key.Name, UID: uid},
		CheckedAt:  time.Now().UTC(),
	}
	stop := func(reason string) (PretrialFailureInspection, error) {
		proof.Reason = reason
		return proof, errors.New(reason)
	}
	if ctx.Err() != nil {
		return stop("inspection canceled")
	}
	if reader == nil || objects == nil || key.Namespace == "" || key.Name == "" || uid == "" || logicalStore == "" || strings.TrimSpace(logicalStore) != logicalStore {
		return stop("inspection requires readers, logical store, namespace, name, and UID")
	}
	parent := &edgev1alpha1.BatchEnergyOptimization{}
	if err := reader.Get(ctx, key, parent); err != nil {
		return stop("cannot read requested BatchEnergyOptimization")
	}
	proof.Request = PretrialInspectionRequest{Namespace: parent.Namespace, Name: parent.Name, UID: parent.UID, ResourceVersion: parent.ResourceVersion, Generation: parent.Generation}
	if parent.UID != uid {
		return stop("requested BatchEnergyOptimization UID changed")
	}
	proof.PublishedResultAbsent = parent.Status.Result == nil
	if !proof.PublishedResultAbsent {
		return stop("published result exists")
	}
	if !pretrialRequestUnstarted(parent) {
		return stop("request is not unstarted")
	}
	if len(validation.IsValidLabelValue(parent.Name)) == 0 {
		return stop("campaign label metadata is valid")
	}
	runs := &edgev1alpha1.ProfilingRunList{}
	if err := reader.List(ctx, runs, client.InNamespace(parent.Namespace)); err != nil {
		return stop("cannot list namespace ProfilingRuns")
	}
	for i := range runs.Items {
		if controlledByParent(parent, &runs.Items[i]) {
			proof.OwnedRunCount++
		}
	}
	if proof.OwnedRunCount != 0 {
		return stop("controller-owned ProfilingRun exists")
	}
	if reason := pretrialPlanAbsenceFailure(ctx, objects, curvePlanLocation(logicalStore, parent.UID)); reason != "" {
		return stop(reason)
	}
	proof.PlanNotFound = true
	granted, at, _ := profilingrun.Admission(parent.Annotations)
	proof.Admission = PretrialInspectionAdmission{Granted: granted, At: at}
	if !granted || at.IsZero() {
		return stop("valid admission grant is missing")
	}
	if parent.Spec.State.TargetNode == "" {
		return stop("target Node is missing")
	}
	node := &corev1.Node{}
	if err := reader.Get(ctx, types.NamespacedName{Name: parent.Spec.State.TargetNode}, node); err != nil {
		return stop("cannot read target Node")
	}
	holder, held := profilingrun.MeasurementHolder(node)
	proof.Node = PretrialInspectionNode{Name: node.Name, UID: node.UID, ResourceVersion: node.ResourceVersion, Holder: holder}
	if reason := pretrialNodeFailure(parent, node, holder, held); reason != "" {
		return stop(reason)
	}
	if ctx.Err() != nil {
		return stop("inspection canceled")
	}
	proof.Eligible = true
	proof.Reason = profiler.ResolutionReasonInvalidRunSpec
	return proof, nil
}

func pretrialRequestUnstarted(parent *edgev1alpha1.BatchEnergyOptimization) bool {
	return parent.DeletionTimestamp.IsZero() && parent.Generation >= 1 && parent.Status.Phase == "" &&
		parent.Status.CompletedTrials == 0 && parent.Status.Outcome == "" && len(parent.Status.Conditions) == 0 &&
		parent.Status.ObservedGeneration == 0
}

func pretrialPlanAbsenceFailure(ctx context.Context, objects evidencestore.LocationReader, location evidencestore.ObjectLocation) string {
	if err := location.Validate(); err != nil {
		return "recorded plan location is invalid"
	}
	body, err := objects.OpenLocation(ctx, location)
	if body != nil {
		closeErr := body.Close()
		if closeErr != nil {
			return "cannot close recorded plan read"
		}
		if err != nil {
			return "recorded plan read returned contradictory evidence"
		}
	}
	if err == nil {
		return "recorded plan exists or absence is unproven"
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchBucket" {
		return "recorded plan bucket is missing"
	}
	if !errors.Is(err, evidencestore.ErrNotFound) {
		return "cannot read recorded plan"
	}
	return ""
}

func pretrialNodeFailure(parent *edgev1alpha1.BatchEnergyOptimization, node *corev1.Node, holder string, held bool) string {
	if node.Name != parent.Spec.State.TargetNode || node.UID == "" || !node.DeletionTimestamp.IsZero() || !held || holder != string(parent.UID) {
		return "target Node identity or measurement holder does not match"
	}
	// Duplicate measurement taints are ambiguous even if the first one matches.
	holders := 0
	for _, taint := range node.Spec.Taints {
		if taint.Key == profilingrun.MeasurementTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
			holders++
		}
	}
	if holders != 1 {
		return "target Node measurement holder is ambiguous"
	}
	return ""
}
