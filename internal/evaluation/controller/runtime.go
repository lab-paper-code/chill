// Package controller executes the bounded EvaluationRun/EvaluationStage protocol.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

const configName = "chill-evaluation-config"
const maximumResultBytes int64 = 4 << 20
const (
	complete           = "Complete"
	injectionInvalid   = "INVALID"
	injectionUnknown   = "Unknown"
	injectionRequested = "Requested"
	actionUncordon     = "uncordon"
	actionCordonDelete = "cordon-delete"
)

var pending = ctrl.Result{RequeueAfter: time.Second}
var errExecutionLost = errors.New(api.EvaluationReasonExecutionLost)

// ObjectStore uses independent bounded reads, never Job exit status as evidence.
type ObjectStore interface {
	evidencestore.LocationReader
	evidencestore.ImmutableWriter
}

type Runtime struct {
	Client    client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Store     func(context.Context, string, string, map[string]string) (ObjectStore, error)
	Now       func() time.Time
	// HTTP is the authenticated Chill API reader; mutations use Kubernetes only.
	HTTP func(context.Context, string, map[string]string, string, any) error
}

func (r *Runtime) now() metav1.Time {
	if r.Now != nil {
		return metav1.NewTime(r.Now().UTC())
	}
	return metav1.Now()
}

func (r *Runtime) patchStatus(ctx context.Context, object, before client.Object) error {
	return r.Client.Status().Patch(ctx, object, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}
func ref(object client.Object) *api.EvaluationObjectReference {
	return &api.EvaluationObjectReference{Name: object.GetName(), UID: object.GetUID()}
}
func uidName(uid types.UID) string {
	return "eval-" + strings.ToLower(strings.ReplaceAll(string(uid), "-", ""))
}
func condition(conditions *[]metav1.Condition, generation int64, kind string, status metav1.ConditionStatus, reason, message string) {
	if reason == "" {
		reason = complete
	}
	meta.SetStatusCondition(conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message, ObservedGeneration: generation})
}
func isTrue(conditions []metav1.Condition, kind string) bool {
	return meta.IsStatusConditionTrue(conditions, kind)
}
func terminal(conditions []metav1.Condition, kind string) bool {
	c := meta.FindStatusCondition(conditions, kind)
	return c != nil && c.Status != metav1.ConditionUnknown
}
func procedure(procedures *[]api.EvaluationProcedure, step string) *api.EvaluationProcedure {
	for i := range *procedures {
		if (*procedures)[i].Step == step {
			return &(*procedures)[i]
		}
	}
	*procedures = append(*procedures, api.EvaluationProcedure{Step: step})
	return &(*procedures)[len(*procedures)-1]
}
func findProcedure(procedures []api.EvaluationProcedure, step string) *api.EvaluationProcedure {
	for i := range procedures {
		if procedures[i].Step == step {
			return &procedures[i]
		}
	}
	return nil
}
func jsonString(value any) (string, error) { b, e := json.Marshal(value); return string(b), e }

// SetupWithManager registers only under the existing profiler leader election.
func SetupWithManager(mgr ctrl.Manager, enabled bool, runtime Runtime) error {
	if !enabled {
		return nil
	}
	runtime.Client, runtime.APIReader, runtime.Scheme = mgr.GetClient(), mgr.GetAPIReader(), mgr.GetScheme()
	if err := ctrl.NewControllerManagedBy(mgr).For(&api.EvaluationRun{}).Owns(&api.EvaluationStage{}).Owns(&batchv1.Job{}).WithOptions(controller.Options{MaxConcurrentReconciles: 1}).Complete(&RunReconciler{Runtime: runtime}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).For(&api.EvaluationStage{}).Owns(&batchv1.Job{}).WithOptions(controller.Options{MaxConcurrentReconciles: 1}).Complete(&StageReconciler{Runtime: runtime})
}

type stepResult struct {
	SchemaVersion string    `json:"schemaVersion"`
	RunUID        types.UID `json:"runUID"`
	StageUID      types.UID `json:"stageUID,omitempty"`
	StageID       string    `json:"stageID,omitempty"`
	Step          string    `json:"step"`
	Execution     struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"execution"`
	Measurement *struct {
		Verdict api.MeasurementVerdict `json:"verdict"`
		Reason  string                 `json:"reason"`
	} `json:"measurement,omitempty"`
	Cleanup *struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"cleanup,omitempty"`
	StartedAt  metav1.Time                                `json:"startedAt"`
	FinishedAt metav1.Time                                `json:"finishedAt"`
	Artifacts  []api.EvaluationEvidenceReference          `json:"artifacts"`
	Outputs    map[string]api.EvaluationEvidenceReference `json:"outputs,omitempty"`
}

const scopeRun = "run"

func scope(stageID string) string {
	if stageID == "" {
		return scopeRun
	}
	return "stages/" + stageID
}
func evidenceKey(runUID types.UID, suffix string) string {
	return "experiments/" + string(runUID) + "/" + suffix
}
func readJSON(ctx context.Context, store ObjectStore, logicalStore, key string, value any) (*api.EvaluationEvidenceReference, error) {
	stream, err := store.OpenLocation(ctx, evidencestore.ObjectLocation{Store: logicalStore, Key: key})
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	b, err := io.ReadAll(io.LimitReader(stream, maximumResultBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maximumResultBytes {
		return nil, fmt.Errorf("%s: evidence exceeds result limit", api.EvaluationReasonEvidenceUnreadable)
	}
	if err = json.Unmarshal(b, value); err != nil {
		return nil, fmt.Errorf("%s: %w", api.EvaluationReasonEvidenceUnreadable, err)
	}
	return &api.EvaluationEvidenceReference{Store: logicalStore, Key: key, Size: int64(len(b))}, nil
}
func writeJSON(ctx context.Context, store ObjectStore, logicalStore, key string, value any) (*api.EvaluationEvidenceReference, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	identity := evidencestore.ObjectIdentity{Store: logicalStore, Key: key, Size: int64(len(b))}
	if _, err = store.PutImmutable(ctx, identity, bytes.NewReader(b)); err != nil {
		return nil, err
	}
	// Independently open the saved object; a receipt is not completion evidence.
	var saved json.RawMessage
	return readJSON(ctx, store, logicalStore, key, &saved)
}

func (r *Runtime) ensureConfigMap(ctx context.Context, owner client.Object, name string, data map[string]string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: owner.GetNamespace(), Name: name}, Immutable: ptr(true), Data: data}
	if err := controllerutil.SetControllerReference(owner, cm, r.Scheme); err != nil {
		return nil, err
	}
	existing := &corev1.ConfigMap{}
	err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(cm), existing)
	if apierrors.IsNotFound(err) {
		err = r.Client.Create(ctx, cm)
		if err == nil {
			return cm, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		err = r.APIReader.Get(ctx, client.ObjectKeyFromObject(cm), existing)
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(existing, owner) || existing.Immutable == nil || !*existing.Immutable || !equality.Semantic.DeepEqual(existing.Data, data) {
		return nil, fmt.Errorf("%s: immutable ConfigMap %s differs", api.EvaluationReasonIdentityMismatch, name)
	}
	return existing, nil
}
func ptr[T any](v T) *T { return &v }

type auditRecord struct {
	RunUID     types.UID       `json:"runUID"`
	StageUID   types.UID       `json:"stageUID,omitempty"`
	Annotation string          `json:"annotation,omitempty"`
	At         metav1.Time     `json:"at"`
	Details    json.RawMessage `json:"details"`
}

// audit recovers an already saved record after a status/response loss by identity,
// without rewriting it or treating byte equivalence as conflict recovery.
func (r *Runtime) audit(ctx context.Context, run *api.EvaluationRun, stageUID types.UID, suffix, annotation string, details any) (*api.EvaluationEvidenceReference, error) {
	store, err := r.Store(ctx, run.Namespace, run.Spec.EvidenceStore, run.Status.FrozenConfig.Values)
	if err != nil {
		return nil, err
	}
	key := evidenceKey(run.UID, suffix)
	var saved auditRecord
	reference, err := readJSON(ctx, store, run.Spec.EvidenceStore, key, &saved)
	if err == nil {
		if saved.RunUID != run.UID || saved.StageUID != stageUID || saved.Annotation != annotation || saved.At.IsZero() {
			return nil, fmt.Errorf("audit identity mismatch")
		}
		return reference, nil
	}
	if !errors.Is(err, evidencestore.ErrNotFound) {
		return nil, err
	}
	body, err := json.Marshal(details)
	if err != nil {
		return nil, err
	}
	return writeJSON(ctx, store, run.Spec.EvidenceStore, key, auditRecord{RunUID: run.UID, StageUID: stageUID, Annotation: annotation, At: r.now(), Details: body})
}
