package kube

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	"github.com/lab-paper-code/chill/internal/serving"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operationModelUpdate        = "ModelUpdate"
	operationPreprocessorUpdate = "PreprocessorUpdate"
	operationDelete             = "Delete"
	operationCompleted          = "Completed"
	operationFailed             = "Failed"
)

type ManagerConfig struct{ DrainTimeout time.Duration }

// Manager is the only definition writer. It observes drain without holding this
// lock across a wait; Advance performs one finite observation/application pass.
// ponytail: one management lock and native list scans suit research-scale edits.
// Split locks only if management contention is measured.
type Manager struct {
	mu          sync.Mutex
	store       *Store
	runtime     *serving.Runtime
	journal     *serving.Journal
	observer    *Observer
	config      ManagerConfig
	initialized bool
	// A failed status write must not let a subsequent poll resume the operation.
	// Only terminal failures are retained here; native status owns normal progress.
	failed map[types.UID]api.WorkloadStatus
	// This is only proof of a deletion already performed, never a workflow to
	// replay. The native object may disappear before its confirming GET succeeds.
	deletionChecks map[types.UID]*api.Workload
}

func NewManager(store *Store, runtime *serving.Runtime, journal *serving.Journal, observer *Observer, config ManagerConfig) (*Manager, error) {
	if store == nil || store.client == nil || runtime == nil || journal == nil || observer == nil || config.DrainTimeout <= 0 {
		return nil, errors.New("native store, runtime, journal, observer and positive drain timeout are required")
	}
	return &Manager{store: store, runtime: runtime, journal: journal, observer: observer, config: config,
		failed: map[types.UID]api.WorkloadStatus{}, deletionChecks: map[types.UID]*api.Workload{}}, nil
}

func managementError(code, message, field string) error {
	return &serving.Error{Code: code, Message: message, Field: field}
}

func failureReason(err error) string {
	var e *serving.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if reason := apierrors.ReasonForError(err); reason != metav1.StatusReasonUnknown {
		return string(reason)
	}
	return "ManagementOperationFailed"
}

func active(op *api.WorkloadOperation) bool {
	return op != nil && op.Phase != operationCompleted && op.Phase != operationFailed
}

func (m *Manager) status(w *api.Workload) api.WorkloadStatus {
	if status, ok := m.failed[w.UID]; ok {
		return *status.DeepCopy()
	}
	return *w.Status.DeepCopy()
}

func (m *Manager) applied(w *api.Workload) *serving.Definition {
	state, err := m.runtime.State(w.Name)
	if err == nil && state.Applied != nil && state.Applied.UID == string(w.UID) {
		return state.Applied
	}
	return nil
}

func appliedStatus(d *serving.Definition) *api.WorkloadAppliedDefinition {
	if d == nil {
		return nil
	}
	status := &api.WorkloadAppliedDefinition{
		WorkloadGeneration: d.Generation,
		ModelRef:           api.WorkloadAppliedCatalogReference{Name: d.Model.Name, UID: types.UID(d.Model.UID), Generation: d.Model.Generation},
		PreprocessorRef:    api.WorkloadAppliedCatalogReference{Name: d.Preprocessor.Name, UID: types.UID(d.Preprocessor.UID), Generation: d.Preprocessor.Generation},
		SLO:                api.WorkloadSLO{Percentile: d.SLO.Percentile, TargetLatency: metav1.Duration{Duration: d.SLO.TargetLatency}},
	}
	if d.Dataset != nil {
		status.DatasetRef = &api.WorkloadAppliedDatasetReference{Name: d.Dataset.Name, UID: types.UID(d.Dataset.UID)}
	}
	return status
}

func (m *Manager) persist(ctx context.Context, w *api.Workload, status api.WorkloadStatus) error {
	if d := m.applied(w); d != nil {
		status.AppliedDefinition = appliedStatus(d)
	}
	if state, err := m.runtime.State(w.Name); err == nil && state.Applied != nil && state.Applied.UID == string(w.UID) {
		status.Admission = api.WorkloadAdmission{Allowed: state.Admission, Reason: state.Reason, ObservedAt: metav1.NewTime(state.ObservedAt)}
	} else {
		status.Admission = api.WorkloadAdmission{Reason: "AppliedDefinitionUnavailable", ObservedAt: metav1.Now()}
	}
	old := w.Status
	// Admission timestamps are observations, not a per-poll status write trigger.
	old.Admission.ObservedAt = status.Admission.ObservedAt
	if reflect.DeepEqual(old, status) {
		return nil
	}
	updated, err := m.store.UpdateWorkloadStatus(ctx, w, status)
	if err == nil {
		*w = *updated
	}
	return err
}

func (m *Manager) record(ctx context.Context, w *api.Workload, phase string, before, target, after *serving.Definition, at time.Time, err error) error {
	kind := "Registration"
	actor := PrincipalFromContext(ctx)
	if op := m.status(w).Operation; op != nil {
		kind = op.Kind
		// Polling and restart must retain the original accepted-operation actor.
		actor = Principal{username: op.ActorUsername, uid: op.ActorUID}
	}
	event := serving.ManagementEvent{WorkloadUID: string(w.UID), Kind: kind, Phase: phase, Before: before, Target: target, After: after, At: at,
		ActorUsername: actor.username, ActorUID: actor.uid}
	if err != nil {
		event.Error = failureReason(err)
	}
	return m.journal.ManagementEvent(event)
}

func (m *Manager) fail(ctx context.Context, w *api.Workload, cause error) error {
	_ = m.runtime.StopAdmission(string(w.UID), failureReason(cause))
	status := m.status(w)
	if status.Operation == nil {
		actor := PrincipalFromContext(ctx)
		status.Operation = &api.WorkloadOperation{Kind: "Update", TargetGeneration: w.Generation, AcceptedAt: metav1.Now(), ActorUsername: actor.username, ActorUID: actor.uid}
	}
	failedPhase := status.Operation.Phase
	status.Operation.Phase = operationFailed
	status.Operation.Reason = failureReason(cause)
	status.Operation.Error = failureReason(cause)
	if failedPhase != "" && failedPhase != operationFailed {
		status.Operation.Error += " during " + failedPhase
	}
	now := metav1.Now()
	status.Operation.CompletedAt = &now
	if d := m.applied(w); d != nil {
		status.AppliedDefinition = appliedStatus(d)
	}
	status.Admission = api.WorkloadAdmission{Reason: failureReason(cause), ObservedAt: now}
	m.failed[w.UID] = status
	writeErr := m.persist(ctx, w, status)
	if writeErr == nil {
		delete(m.failed, w.UID)
	}
	journalErr := m.record(ctx, w, operationFailed, nil, nil, m.applied(w), now.Time, cause)
	return errors.Join(cause, writeErr, journalErr)
}

func (m *Manager) failAccepted(ctx context.Context, w *api.Workload, status api.WorkloadStatus, cause error) error {
	m.failed[w.UID] = status
	return m.fail(ctx, w, cause)
}

func (m *Manager) failGroup(ctx context.Context, workloads []*api.Workload, cause error) error {
	// Close the entire barrier before persisting any partial failure.
	for _, w := range workloads {
		_ = m.runtime.StopAdmission(string(w.UID), failureReason(cause))
	}
	errs := []error{cause}
	for _, w := range workloads {
		errs = append(errs, m.fail(ctx, w, cause))
	}
	return errors.Join(errs...)
}

func (m *Manager) requireReady() error {
	if !m.initialized {
		return managementError("ManagementUnavailable", "management initialization has not completed", "")
	}
	return nil
}

func checkVersion(current, requested string) error {
	if requested == "" || current != requested {
		return managementError("ResourceVersionConflict", "resourceVersion is missing or stale", "resourceVersion")
	}
	return nil
}

func (m *Manager) checkOperation(w *api.Workload) error {
	if active(m.status(w).Operation) {
		return managementError("OperationConflict", "a management operation is already active", "operation")
	}
	return nil
}

func validateModel(model *api.ModelSpec) error {
	if messages := validation.IsDNS1123Subdomain(model.Name); len(messages) != 0 {
		return managementError("InvalidModel", fmt.Sprint(messages), "metadata.name")
	}
	if len(model.Spec.Artifacts) == 0 || len(model.Spec.ExecutionPaths) == 0 {
		return managementError("InvalidModel", "artifacts and executionPaths must not be empty", "spec")
	}
	for _, artifact := range model.Spec.Artifacts {
		if artifact.Name == "" || artifact.Format == "" {
			return managementError("InvalidModel", "artifact name and format are required", "spec.artifacts")
		}
	}
	for _, path := range model.Spec.ExecutionPaths {
		if path.Name == "" || path.Runtime.Family == "" || path.Runtime.Backend == "" {
			return managementError("InvalidModel", "execution path name, runtime family and backend are required", "spec.executionPaths")
		}
	}
	if err := modelcatalog.Validate(model); err != nil {
		return managementError("InvalidModel", err.Error(), "spec")
	}
	return nil
}

// catalogRefs holds the catalog objects one Workload spec names.
type catalogRefs struct {
	model        *api.ModelSpec
	preprocessor *api.Preprocessor
	dataset      *api.Dataset
}

// snapshot keeps only the execution paths whose artifact the preprocessor
// provides code for (WM-08).
func snapshot(w *api.Workload, refs catalogRefs) (*serving.Definition, error) {
	model, p := refs.model, refs.preprocessor
	if err := validateModel(model); err != nil {
		return nil, err
	}
	if w.UID == "" || model.UID == "" || p.UID == "" {
		return nil, managementError("IdentityUnavailable", "native registration UID is unavailable", "metadata.uid")
	}
	d := &serving.Definition{Name: w.Name, UID: string(w.UID), Generation: w.Generation,
		Model:        serving.Model{Name: model.Name, UID: string(model.UID), Generation: model.Generation},
		Preprocessor: serving.Preprocessor{Name: p.Name, UID: string(p.UID), Generation: p.Generation},
		SLO:          serving.SLO{Percentile: w.Spec.SLO.Percentile, TargetLatency: w.Spec.SLO.TargetLatency.Duration}}
	for _, field := range p.Spec.Input.Fields {
		d.Preprocessor.Input = append(d.Preprocessor.Input, serving.RawField{Name: field.Name, Format: field.Format, MaxBytes: field.MaxBytes})
	}
	for _, field := range p.Spec.Output.Fields {
		d.Preprocessor.Output = append(d.Preprocessor.Output, serving.RawField{Name: field.Name, Format: field.Format})
	}
	if p.Spec.MaxNewTokens != nil {
		d.Preprocessor.MaxNewTokens = *p.Spec.MaxNewTokens
	}
	if refs.dataset != nil {
		d.Dataset = &serving.DatasetReference{Name: refs.dataset.Name, UID: string(refs.dataset.UID)}
	}
	for _, path := range model.Spec.ExecutionPaths {
		for _, artifact := range model.Spec.Artifacts {
			if artifact.Name != path.Artifact {
				continue
			}
			code, ok := modelcatalog.PreprocessorArtifact(p, model.Name, artifact.Name)
			if ok {
				d.Model.Paths = append(d.Model.Paths, serving.ModelPath{Name: path.Name, Artifact: artifact.Name, Format: artifact.Format,
					Image: artifact.Source.Image, Architecture: artifact.Source.Architecture, Path: artifact.Source.Path,
					RuntimeFamily: path.Runtime.Family, Backend: path.Runtime.Backend, Tensors: modelcatalog.InputTensors(artifact.Input), Firstfloor: modelcatalog.FirstfloorSettings(path.Runtime),
					Preprocessor: serving.PreprocessorArtifact{Image: code.Source.Image, Architecture: code.Source.Architecture, Path: code.Source.Path}})
			}
			break
		}
	}
	return d, nil
}

// catalogBusy reports an active shared update targeting the catalog object.
func (m *Manager) catalogBusy(workloads *api.WorkloadList, uid types.UID) bool {
	for i := range workloads.Items {
		op := m.status(&workloads.Items[i]).Operation
		if active(op) && op.TargetCatalogUID == uid {
			return true
		}
	}
	return false
}

// checkReference refuses a missing or deleting catalog reference.
func checkReference(obj client.Object, err error, kind, field string) error {
	if apierrors.IsNotFound(err) {
		return managementError("Invalid"+kind+"Reference", "referenced "+kind+" does not exist", field)
	}
	if err != nil {
		return err
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		return managementError(kind+"Deleting", "referenced "+kind+" is being deleted", field)
	}
	return nil
}

// loadReferences reads the catalog objects a Workload spec names and checks
// them and the dataset and preprocessor compatibility (WM-06, DP-07).
func (m *Manager) loadReferences(ctx context.Context, spec api.WorkloadSpec) (catalogRefs, error) {
	var refs catalogRefs
	var err error
	refs.model, err = m.store.GetModel(ctx, spec.ModelRef.Name)
	if err := checkReference(refs.model, err, "Model", "spec.modelRef.name"); err != nil {
		return refs, err
	}
	if err := validateModel(refs.model); err != nil {
		return refs, err
	}
	refs.preprocessor, err = m.store.GetPreprocessor(ctx, spec.PreprocessorRef.Name)
	if err := checkReference(refs.preprocessor, err, "Preprocessor", "spec.preprocessorRef.name"); err != nil {
		return refs, err
	}
	if spec.DatasetRef == nil {
		return refs, nil
	}
	refs.dataset, err = m.store.GetDataset(ctx, spec.DatasetRef.Name)
	if err := checkReference(refs.dataset, err, "Dataset", "spec.datasetRef.name"); err != nil {
		return refs, err
	}
	if mismatched := modelcatalog.Compatible(refs.dataset, refs.preprocessor); len(mismatched) != 0 {
		return refs, managementError("IncompatibleDataset", fmt.Sprintf("dataset records do not satisfy preprocessor input fields %v", mismatched), "spec.datasetRef.name")
	}
	return refs, nil
}

func (m *Manager) validatedReferences(ctx context.Context, name string, spec api.WorkloadSpec) (catalogRefs, error) {
	if err := api.ValidateWorkload(name, spec); err != nil {
		fieldName := "spec"
		if aggregate, ok := err.(utilerrors.Aggregate); ok {
			for _, cause := range aggregate.Errors() {
				if invalid, ok := cause.(*field.Error); ok {
					fieldName = invalid.Field
					break
				}
			}
		}
		return catalogRefs{}, managementError("InvalidWorkload", err.Error(), fieldName)
	}
	refs, err := m.loadReferences(ctx, spec)
	if err != nil {
		return refs, err
	}
	workloads, err := m.store.ListWorkloads(ctx)
	if err != nil {
		return refs, err
	}
	if m.catalogBusy(workloads, refs.model.UID) {
		return refs, managementError("OperationConflict", "referenced ModelSpec has an active shared update", "spec.modelRef.name")
	}
	if m.catalogBusy(workloads, refs.preprocessor.UID) {
		return refs, managementError("OperationConflict", "referenced Preprocessor has an active shared update", "spec.preprocessorRef.name")
	}
	return refs, nil
}

// Initialize restores only recorded applied snapshots. Interrupted operations
// become terminal/closed; neither desired targets nor inference are replayed.
func (m *Manager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.initialized {
		return nil
	}
	list, err := m.store.ListWorkloads(ctx)
	if err != nil {
		return err
	}
	interruptedModels, interruptedPreprocessors := map[string]bool{}, map[string]bool{}
	for i := range list.Items {
		w := &list.Items[i]
		if op := w.Status.Operation; active(op) && op.Kind == operationModelUpdate {
			interruptedModels[w.Spec.ModelRef.Name] = true
		} else if active(op) && op.Kind == operationPreprocessorUpdate {
			interruptedPreprocessors[w.Spec.PreprocessorRef.Name] = true
		}
	}
	var errs []error
	for i := range list.Items {
		w := &list.Items[i]
		if err := m.restoreWorkload(ctx, w, interruptedModels[w.Spec.ModelRef.Name] || interruptedPreprocessors[w.Spec.PreprocessorRef.Name]); err != nil {
			err = m.fail(ctx, w, err)
			if _, unwritten := m.failed[w.UID]; unwritten || !m.journal.Health().Healthy {
				errs = append(errs, err)
			}
		}
	}
	m.initialized = true
	return errors.Join(errs...)
}

func (m *Manager) restoreDefinition(ctx context.Context, w *api.Workload, status api.WorkloadStatus, interrupted bool) (*serving.Definition, error) {
	if a := status.AppliedDefinition; a != nil {
		identity := (&serving.Definition{UID: string(w.UID), Generation: a.WorkloadGeneration,
			Model:        serving.Model{UID: string(a.ModelRef.UID), Generation: a.ModelRef.Generation},
			Preprocessor: serving.Preprocessor{UID: string(a.PreprocessorRef.UID), Generation: a.PreprocessorRef.Generation}}).Identity()
		d := m.runtime.RetainedDefinition(identity)
		if d == nil {
			return nil, managementError("AppliedSnapshotUnavailable", "recorded applied snapshot is unavailable", "status.appliedDefinition")
		}
		return d, nil
	}
	if m.applied(w) != nil {
		return nil, managementError("AppliedStateUnconfirmed", "runtime facts have no confirmed applied status", "status.appliedDefinition")
	}
	if interrupted || status.Operation != nil && status.Operation.Phase == operationFailed {
		return nil, managementError("EntrypointRestarted", "interrupted operation requires an explicit new management request", "operation")
	}
	refs, err := m.validatedReferences(ctx, w.Name, w.Spec)
	if err != nil {
		return nil, err
	}
	return snapshot(w, refs)
}

func (m *Manager) restoreWorkload(ctx context.Context, w *api.Workload, sharedInterrupted bool) error {
	if w.DeletionTimestamp.IsZero() {
		if err := m.store.EnsureWorkloadFinalizer(ctx, w); err != nil {
			return err
		}
	}
	status := m.status(w)
	interrupted := active(status.Operation) || sharedInterrupted || !w.DeletionTimestamp.IsZero()
	d, err := m.restoreDefinition(ctx, w, status, interrupted)
	if err != nil {
		return err
	}
	if err := m.runtime.PublishClosed(d, "RestartDrain"); err != nil {
		return err
	}
	if interrupted {
		return managementError("EntrypointRestarted", "interrupted operation requires an explicit new management request", "operation")
	}
	if status.Operation != nil && status.Operation.Phase == operationFailed {
		_ = m.runtime.StopAdmission(string(w.UID), status.Operation.Reason)
	} else {
		refs, err := m.loadReferences(ctx, w.Spec)
		var want *serving.Definition
		if err == nil {
			want, err = snapshot(w, refs)
		}
		if err != nil || want.Identity() != d.Identity() || want.SLO != d.SLO || !reflect.DeepEqual(want.Dataset, d.Dataset) {
			return managementError("AppliedStateUnconfirmed", "desired and recorded applied definitions differ", "status.appliedDefinition")
		}
		if err := m.persist(ctx, w, status); err != nil {
			return err
		}
		if m.runtime.Drained(string(w.UID)) {
			if err := m.runtime.OpenAdmission(string(w.UID)); err != nil {
				return err
			}
		}
	}
	return m.persist(ctx, w, status)
}

func (m *Manager) CreateWorkload(ctx context.Context, name string, spec api.WorkloadSpec) (WorkloadView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return WorkloadView{}, err
	}
	for _, pending := range m.deletionChecks {
		if pending.Name == name {
			return WorkloadView{}, managementError("RegistrationConflict", "previous registration deletion is not yet confirmed", "metadata.name")
		}
	}
	refs, err := m.validatedReferences(ctx, name, spec)
	if err != nil {
		return WorkloadView{}, err
	}
	w := &api.Workload{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
	if err := m.store.CreateWorkload(ctx, w); err != nil {
		return WorkloadView{}, err
	}
	d, err := snapshot(w, refs)
	if err == nil {
		err = m.record(ctx, w, "Accepted", nil, d, nil, time.Now().UTC(), nil)
	}
	if err == nil {
		err = m.runtime.PublishClosed(d, "Applying")
	}
	if err == nil {
		err = m.persist(ctx, w, w.Status)
	}
	if err == nil {
		err = m.record(ctx, w, "Applied", nil, d, d, time.Now().UTC(), nil)
	}
	if err == nil {
		err = m.runtime.OpenAdmission(string(w.UID))
	}
	if err == nil {
		err = m.persist(ctx, w, w.Status)
	}
	if err == nil {
		err = m.record(ctx, w, operationCompleted, nil, d, d, time.Now().UTC(), nil)
	}
	if err != nil {
		err = m.fail(ctx, w, err)
		return m.view(w), err
	}
	return m.view(w), nil
}

func (m *Manager) UpdateWorkload(ctx context.Context, name, resourceVersion string, spec api.WorkloadSpec) (WorkloadView, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return WorkloadView{}, false, err
	}
	w, err := m.store.GetWorkload(ctx, name)
	if err != nil {
		return WorkloadView{}, false, err
	}
	if err := checkVersion(w.ResourceVersion, resourceVersion); err != nil {
		return m.view(w), false, err
	}
	if !w.DeletionTimestamp.IsZero() {
		return m.view(w), false, managementError("OperationConflict", "Workload is being deleted", "operation")
	}
	if err := m.checkOperation(w); err != nil {
		return m.view(w), false, err
	}
	refs, err := m.validatedReferences(ctx, name, spec)
	if err != nil {
		return m.view(w), false, err
	}
	before := m.applied(w)
	w.Spec = spec
	if err := m.store.UpdateWorkload(ctx, w); err != nil {
		return m.view(w), false, err
	}
	delete(m.failed, w.UID)
	status := w.Status
	actor := PrincipalFromContext(ctx)
	status.Operation = &api.WorkloadOperation{Kind: "Update", Phase: "Draining", TargetGeneration: w.Generation, AcceptedAt: metav1.Now(), ActorUsername: actor.username, ActorUID: actor.uid}
	d, err := snapshot(w, refs)
	if err != nil {
		err = m.failAccepted(ctx, w, status, err)
		return m.view(w), false, err
	}
	if err := m.stop(w, "Changing"); err != nil {
		err = m.failAccepted(ctx, w, status, err)
		return m.view(w), false, err
	}
	if err := m.persist(ctx, w, status); err != nil {
		err = m.failAccepted(ctx, w, status, err)
		return m.view(w), false, err
	}
	if err := m.record(ctx, w, "Accepted", before, d, nil, status.Operation.AcceptedAt.Time, nil); err != nil {
		err = m.fail(ctx, w, err)
		return m.view(w), false, err
	}
	// SLO and dataset changes keep the accepted execution and apply without drain (WM-16).
	if before != nil && before.SameExecution(d) {
		if err := m.apply(ctx, w, d); err != nil {
			err = m.fail(ctx, w, err)
			return m.view(w), false, err
		}
		return m.view(w), false, nil
	}
	return m.view(w), true, nil
}

func (m *Manager) stop(w *api.Workload, reason string) error {
	if m.applied(w) == nil {
		return nil
	}
	return m.runtime.StopAdmission(string(w.UID), reason)
}

func (m *Manager) DeleteWorkload(ctx context.Context, name, resourceVersion string) (WorkloadView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return WorkloadView{}, err
	}
	w, err := m.store.GetWorkload(ctx, name)
	if err != nil {
		return WorkloadView{}, err
	}
	if err := checkVersion(w.ResourceVersion, resourceVersion); err != nil {
		return m.view(w), err
	}
	if err := m.checkOperation(w); err != nil {
		return m.view(w), err
	}
	before := m.applied(w)
	if err := m.stop(w, "Deleting"); err != nil {
		return m.view(w), err
	}
	if err := m.store.DeleteWorkload(ctx, w); err != nil {
		actor := PrincipalFromContext(ctx)
		status := w.Status
		status.Operation = &api.WorkloadOperation{Kind: operationDelete, Phase: "Draining", TargetGeneration: w.Generation, AcceptedAt: metav1.Now(), ActorUsername: actor.username, ActorUID: actor.uid}
		err = m.failAccepted(ctx, w, status, err)
		return m.view(w), err
	}
	deletedUID := w.UID
	w, err = m.store.GetWorkload(ctx, name)
	if err != nil {
		return WorkloadView{}, err
	}
	if w.UID != deletedUID {
		return m.view(w), managementError("RegistrationConflict", "registration changed during deletion", "metadata.uid")
	}
	delete(m.failed, w.UID)
	status := w.Status
	actor := PrincipalFromContext(ctx)
	status.Operation = &api.WorkloadOperation{Kind: operationDelete, Phase: "Draining", TargetGeneration: w.Generation, AcceptedAt: metav1.Now(), ActorUsername: actor.username, ActorUID: actor.uid}
	if err := m.persist(ctx, w, status); err != nil {
		err = m.failAccepted(ctx, w, status, err)
		return m.view(w), err
	}
	if err := m.record(ctx, w, "Accepted", before, nil, nil, status.Operation.AcceptedAt.Time, nil); err != nil {
		err = m.fail(ctx, w, err)
		return m.view(w), err
	}
	return m.view(w), nil
}

func (m *Manager) phase(ctx context.Context, w *api.Workload, phase string) error {
	status := m.status(w)
	status.Operation.Phase = phase
	if err := m.persist(ctx, w, status); err != nil {
		return err
	}
	return m.record(ctx, w, phase, nil, nil, m.applied(w), time.Now().UTC(), nil)
}

func (m *Manager) drain(w *api.Workload) (bool, error) {
	if m.runtime.Drained(string(w.UID)) {
		return true, nil
	}
	op := m.status(w).Operation
	if time.Since(op.AcceptedAt.Time) >= m.config.DrainTimeout {
		return false, managementError("DrainTimeout", "accepted requests have not actually drained within the management limit", "operation")
	}
	return false, nil
}

func (m *Manager) install(ctx context.Context, w *api.Workload, d *serving.Definition) error {
	before := m.applied(w)
	if err := m.runtime.PublishClosed(d, "Applying"); err != nil {
		return err
	}
	status := m.status(w)
	now := metav1.Now()
	status.Operation.AppliedAt = &now
	if err := m.persist(ctx, w, status); err != nil {
		return err
	}
	return m.record(ctx, w, "Applied", before, d, d, now.Time, nil)
}

func (m *Manager) complete(ctx context.Context, w *api.Workload) error {
	status := m.status(w)
	status.Operation.Phase = operationCompleted
	now := metav1.Now()
	status.Operation.CompletedAt = &now
	if err := m.persist(ctx, w, status); err != nil {
		return err
	}
	return m.record(ctx, w, operationCompleted, nil, m.applied(w), m.applied(w), now.Time, nil)
}

func (m *Manager) apply(ctx context.Context, w *api.Workload, d *serving.Definition) error {
	if err := m.phase(ctx, w, "Applying"); err != nil {
		return err
	}
	if err := m.install(ctx, w, d); err != nil {
		return err
	}
	if err := m.runtime.OpenAdmission(string(w.UID)); err != nil {
		return err
	}
	return m.complete(ctx, w)
}

func (m *Manager) advanceWorkload(ctx context.Context, w *api.Workload) error {
	op := m.status(w).Operation
	if op.Phase == "Stopping" || op.Phase == "Draining" {
		done, err := m.drain(w)
		if err != nil || !done {
			return err
		}
		if err := m.phase(ctx, w, "Detaching"); err != nil {
			return err
		}
		op = m.status(w).Operation
	}
	if op.Phase == "Detaching" {
		if err := m.runtime.Detach(string(w.UID)); err != nil {
			return err
		}
		if err := m.phase(ctx, w, "Cleaning"); err != nil {
			return err
		}
		op = m.status(w).Operation
	}
	if op.Phase == "Cleaning" {
		done, err := m.observer.CleanupWorkload(ctx, w.UID)
		if err != nil || !done {
			return err
		}
		if op.Kind == operationDelete {
			if err := m.store.RemoveWorkloadFinalizer(ctx, w); err != nil {
				return err
			}
			m.deletionChecks[w.UID] = w.DeepCopy()
			return m.confirmDeletion(ctx, w)
		}
		if err := m.phase(ctx, w, "Applying"); err != nil {
			return err
		}
		op = m.status(w).Operation
	}
	if op.Phase == "Applying" {
		refs, err := m.loadReferences(ctx, w.Spec)
		if err != nil {
			return err
		}
		d, err := snapshot(w, refs)
		if err != nil {
			return err
		}
		return m.apply(ctx, w, d)
	}
	return nil
}

func (m *Manager) confirmDeletion(ctx context.Context, w *api.Workload) error {
	gone, err := m.store.WorkloadDeleted(ctx, w.Name, w.UID)
	if err != nil || !gone {
		return err
	}
	before := m.applied(w)
	if err := m.runtime.Forget(string(w.UID)); err != nil {
		return err
	}
	delete(m.deletionChecks, w.UID)
	delete(m.failed, w.UID)
	return m.record(ctx, w, operationCompleted, before, nil, nil, time.Now().UTC(), nil)
}

func sharedUpdate(op *api.WorkloadOperation) bool {
	return op.Kind == operationModelUpdate || op.Kind == operationPreprocessorUpdate
}

func sameGroup(op, target *api.WorkloadOperation) bool {
	return op != nil && op.Kind == target.Kind && op.TargetCatalogUID == target.TargetCatalogUID && op.TargetCatalogGeneration == target.TargetCatalogGeneration
}

func (m *Manager) advanceGroup(ctx context.Context, group []*api.Workload) error {
	for _, w := range group {
		op := m.status(w).Operation
		if op.Phase == operationFailed {
			return managementError("SharedUpdateFailed", "another member of the shared update barrier failed", "operation")
		}
		done, err := m.drain(w)
		if err != nil || !done {
			return err
		}
	}
	for _, w := range group {
		op := m.status(w).Operation
		if op.Phase == "Stopping" || op.Phase == "Draining" {
			if err := m.phase(ctx, w, "Detaching"); err != nil {
				return err
			}
			op = m.status(w).Operation
		}
		if op.Phase == "Detaching" {
			if err := m.runtime.Detach(string(w.UID)); err != nil {
				return err
			}
			if err := m.phase(ctx, w, "Cleaning"); err != nil {
				return err
			}
		}
	}
	allClean := true
	for _, w := range group {
		if m.status(w).Operation.Phase == "Cleaning" {
			done, err := m.observer.CleanupWorkload(ctx, w.UID)
			if err != nil {
				return err
			}
			allClean = allClean && done
		}
	}
	if !allClean {
		return nil
	}
	// No member publishes until every old ingress/native request, attachment and
	// dedicated executor has crossed the shared barrier.
	target := m.status(group[0]).Operation
	definitions := make([]*serving.Definition, len(group))
	for i, w := range group {
		refs, err := m.loadReferences(ctx, w.Spec)
		if err != nil {
			return err
		}
		uid, generation := refs.model.UID, refs.model.Generation
		if target.Kind == operationPreprocessorUpdate {
			uid, generation = refs.preprocessor.UID, refs.preprocessor.Generation
		}
		if uid != target.TargetCatalogUID || generation != target.TargetCatalogGeneration {
			return managementError("CatalogIdentityConflict", "shared update target identity changed", "operation")
		}
		if definitions[i], err = snapshot(w, refs); err != nil {
			return err
		}
	}
	for i, w := range group {
		if err := m.phase(ctx, w, "Applying"); err != nil {
			return err
		}
		if err := m.install(ctx, w, definitions[i]); err != nil {
			return err
		}
	}
	// Confirm all applied status writes before opening any gate. A failure leaves
	// actual partial application visible, not a fictional rollback.
	for _, w := range group {
		if err := m.runtime.OpenAdmission(string(w.UID)); err != nil {
			return err
		}
	}
	for _, w := range group {
		if err := m.complete(ctx, w); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Advance(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return err
	}
	list, err := m.store.ListWorkloads(ctx)
	if err != nil {
		return err
	}
	visited := map[types.UID]bool{}
	var errs []error
	for uid, w := range m.deletionChecks {
		visited[uid] = true
		if err := m.confirmDeletion(ctx, w); err != nil {
			errs = append(errs, err)
		}
	}
	for i := range list.Items {
		w := &list.Items[i]
		if visited[w.UID] {
			continue
		}
		status := m.status(w)
		if !active(status.Operation) {
			if status.Operation == nil || status.Operation.Phase == operationCompleted {
				state, stateErr := m.runtime.State(w.Name)
				if stateErr == nil && state.Applied.UID == string(w.UID) && state.Reason == "RestartDrain" && m.runtime.Drained(string(w.UID)) {
					stateErr = m.runtime.OpenAdmission(string(w.UID))
				}
				if stateErr == nil {
					stateErr = m.persist(ctx, w, status)
				}
				if stateErr != nil {
					errs = append(errs, m.fail(ctx, w, stateErr))
				}
			}
			continue
		}
		if sharedUpdate(status.Operation) {
			group := []*api.Workload{}
			for j := range list.Items {
				member := &list.Items[j]
				if sameGroup(m.status(member).Operation, status.Operation) {
					group = append(group, member)
					visited[member.UID] = true
				}
			}
			if err := m.advanceGroup(ctx, group); err != nil {
				errs = append(errs, m.failGroup(ctx, group, err))
			}
		} else if err := m.advanceWorkload(ctx, w); err != nil {
			errs = append(errs, m.fail(ctx, w, err))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) ListModels(ctx context.Context) (*api.ModelSpecList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.ListModels(ctx)
}

func (m *Manager) GetModel(ctx context.Context, name string) (*api.ModelSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.GetModel(ctx, name)
}

func (m *Manager) CreateModel(ctx context.Context, name string, spec api.ModelSpecSpec) (*api.ModelSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return nil, err
	}
	model := &api.ModelSpec{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
	if err := validateModel(model); err != nil {
		return nil, err
	}
	if err := m.store.CreateModel(ctx, model); err != nil {
		return nil, err
	}
	return model, nil
}

// holds reports whether a Workload holds a catalog object through its desired
// spec, applied definition or operation target.
type holds func(w *api.Workload, status api.WorkloadStatus, applied *serving.Definition) bool

func holdsModel(model *api.ModelSpec) holds {
	return func(w *api.Workload, status api.WorkloadStatus, applied *serving.Definition) bool {
		return w.Spec.ModelRef.Name == model.Name ||
			(status.AppliedDefinition != nil && status.AppliedDefinition.ModelRef.UID == model.UID) ||
			(applied != nil && applied.Model.UID == string(model.UID)) ||
			(status.Operation != nil && status.Operation.TargetCatalogUID == model.UID)
	}
}

func holdsPreprocessor(p *api.Preprocessor) holds {
	return func(w *api.Workload, status api.WorkloadStatus, applied *serving.Definition) bool {
		return w.Spec.PreprocessorRef.Name == p.Name ||
			(status.AppliedDefinition != nil && status.AppliedDefinition.PreprocessorRef.UID == p.UID) ||
			(applied != nil && applied.Preprocessor.UID == string(p.UID)) ||
			(status.Operation != nil && status.Operation.TargetCatalogUID == p.UID)
	}
}

func holdsDataset(d *api.Dataset) holds {
	return func(w *api.Workload, status api.WorkloadStatus, applied *serving.Definition) bool {
		return (w.Spec.DatasetRef != nil && w.Spec.DatasetRef.Name == d.Name) ||
			(status.AppliedDefinition != nil && status.AppliedDefinition.DatasetRef != nil && status.AppliedDefinition.DatasetRef.UID == d.UID) ||
			(applied != nil && applied.Dataset != nil && applied.Dataset.UID == string(d.UID))
	}
}

func (m *Manager) referencing(list *api.WorkloadList, test holds) []*api.Workload {
	found := []*api.Workload{}
	for i := range list.Items {
		w := &list.Items[i]
		if test(w, m.status(w), m.applied(w)) {
			found = append(found, w)
		}
	}
	return found
}

func workloadNames(workloads []*api.Workload) []string {
	names := make([]string, len(workloads))
	for i, w := range workloads {
		names[i] = w.Name
	}
	sort.Strings(names)
	return names
}

func (m *Manager) referenceWorkloads(ctx context.Context) (*api.WorkloadList, error) {
	list, err := m.store.ListWorkloads(ctx)
	if err != nil {
		return nil, err
	}
	for uid, pending := range m.deletionChecks {
		found := false
		for i := range list.Items {
			found = found || list.Items[i].UID == uid
		}
		if !found {
			list.Items = append(list.Items, *pending.DeepCopy())
		}
	}
	return list, nil
}

func (m *Manager) UpdateModel(ctx context.Context, name, resourceVersion string, spec api.ModelSpecSpec) (*api.ModelSpec, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return nil, false, err
	}
	model, err := m.store.GetModel(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if err := checkVersion(model.ResourceVersion, resourceVersion); err != nil {
		return model, false, err
	}
	if !model.DeletionTimestamp.IsZero() {
		return model, false, managementError("ModelDeleting", "ModelSpec is being deleted", "metadata.name")
	}
	proposed := model.DeepCopy()
	proposed.Spec = spec
	if err := validateModel(proposed); err != nil {
		return model, false, err
	}
	if err := m.keepTargetedArtifacts(ctx, proposed); err != nil {
		return model, false, err
	}
	group, err := m.sharedGroup(ctx, model.UID, holdsModel(model), func(s api.WorkloadSpec) bool { return s.ModelRef.Name == model.Name })
	if err != nil {
		return model, false, err
	}
	if reflect.DeepEqual(model.Spec, spec) {
		if m.settled(group, func(d *serving.Definition) bool { return d.Model.Generation == model.Generation }) {
			return model, false, nil
		}
	} else {
		if err := m.store.UpdateModel(ctx, proposed); err != nil {
			return model, false, err
		}
		model = proposed
	}
	if err := m.startSharedUpdate(ctx, operationModelUpdate, model.UID, model.Generation, group); err != nil {
		return model, false, err
	}
	return model, len(group) != 0, nil
}

// sharedGroup returns the Workloads one shared catalog update drains together.
// desired reports whether a Workload spec still names the updated object.
func (m *Manager) sharedGroup(ctx context.Context, uid types.UID, test holds, desired func(api.WorkloadSpec) bool) ([]*api.Workload, error) {
	list, err := m.referenceWorkloads(ctx)
	if err != nil {
		return nil, err
	}
	if m.catalogBusy(list, uid) {
		return nil, managementError("OperationConflict", "catalog object has an active shared update", "operation")
	}
	group := m.referencing(list, test)
	for _, w := range group {
		if err := m.checkOperation(w); err != nil {
			return nil, err
		}
		if !w.DeletionTimestamp.IsZero() {
			return nil, managementError("OperationConflict", "referencing Workload is being deleted", "operation")
		}
		// Failed replacement away from this object retains its old applied reference;
		// a shared edit must not silently replace that different desired target.
		if !desired(w.Spec) {
			return nil, managementError("OperationConflict", "Workload retains this catalog object while targeting another", "operation")
		}
	}
	return group, nil
}

// settled reports whether every member already applies the current object.
func (m *Manager) settled(group []*api.Workload, current func(*serving.Definition) bool) bool {
	for _, w := range group {
		d, op := m.applied(w), m.status(w).Operation
		if d == nil || !current(d) || (op != nil && op.Phase == operationFailed) {
			return false
		}
	}
	return true
}

// startSharedUpdate closes admission for the whole group and records each
// member's accepted target. Advance drains and applies the group together (WM-16).
func (m *Manager) startSharedUpdate(ctx context.Context, kind string, uid types.UID, generation int64, group []*api.Workload) error {
	acceptedAt := metav1.Now()
	actor := PrincipalFromContext(ctx)
	statuses := make([]api.WorkloadStatus, len(group))
	for i, w := range group {
		delete(m.failed, w.UID)
		statuses[i] = *w.Status.DeepCopy()
		statuses[i].Operation = &api.WorkloadOperation{Kind: kind, Phase: "Draining", TargetGeneration: w.Generation,
			TargetCatalogUID: uid, TargetCatalogGeneration: generation, AcceptedAt: acceptedAt, ActorUsername: actor.username, ActorUID: actor.uid}
	}
	fail := func(cause error) error {
		for i, w := range group {
			m.failed[w.UID] = statuses[i]
		}
		return m.failGroup(ctx, group, cause)
	}
	for _, w := range group {
		if err := m.stop(w, "Changing"); err != nil {
			return fail(err)
		}
	}
	for i, w := range group {
		if err := m.persist(ctx, w, statuses[i]); err != nil {
			return fail(err)
		}
		refs, err := m.loadReferences(ctx, w.Spec)
		if err != nil {
			return fail(err)
		}
		d, err := snapshot(w, refs)
		if err != nil {
			return fail(err)
		}
		if err := m.record(ctx, w, "Accepted", m.applied(w), d, nil, acceptedAt.Time, nil); err != nil {
			return fail(err)
		}
	}
	return nil
}

func (m *Manager) DeleteModel(ctx context.Context, name, resourceVersion string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return err
	}
	model, err := m.store.GetModel(ctx, name)
	if err != nil {
		return err
	}
	if err := checkVersion(model.ResourceVersion, resourceVersion); err != nil {
		return err
	}
	list, err := m.referenceWorkloads(ctx)
	if err != nil {
		return err
	}
	refs := workloadNames(m.referencing(list, holdsModel(model)))
	preprocessors, err := m.preprocessorsTargeting(ctx, model.Name, nil)
	if err != nil {
		return err
	}
	if len(refs) != 0 || len(preprocessors) != 0 {
		return managementError("ModelReferenced", fmt.Sprintf("ModelSpec is referenced by Workloads %v and Preprocessors %v", refs, preprocessors), "metadata.name")
	}
	return m.store.DeleteModel(ctx, model)
}

// preprocessorsTargeting names the Preprocessors with an artifact targeting a
// model artifact absent from kept; nil kept means the whole model is removed.
func (m *Manager) preprocessorsTargeting(ctx context.Context, model string, kept []api.ModelArtifact) ([]string, error) {
	list, err := m.store.ListPreprocessors(ctx)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, p := range list.Items {
		if slices.ContainsFunc(p.Spec.Artifacts, func(a api.PreprocessorArtifact) bool {
			return a.Target.Model == model && !slices.ContainsFunc(kept, func(k api.ModelArtifact) bool { return k.Name == a.Target.Artifact })
		}) {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// keepTargetedArtifacts refuses a model change that removes a Preprocessor target (DP-12).
func (m *Manager) keepTargetedArtifacts(ctx context.Context, proposed *api.ModelSpec) error {
	removed, err := m.preprocessorsTargeting(ctx, proposed.Name, proposed.Spec.Artifacts)
	if err != nil || len(removed) == 0 {
		return err
	}
	return managementError("ModelArtifactReferenced", fmt.Sprintf("removed artifacts are targeted by Preprocessors %v", removed), "spec.artifacts")
}

// validatePreprocessor applies DP-09: the declaration and every target model artifact.
func (m *Manager) validatePreprocessor(ctx context.Context, p *api.Preprocessor) error {
	if messages := validation.IsDNS1123Subdomain(p.Name); len(messages) != 0 {
		return managementError("InvalidPreprocessor", fmt.Sprint(messages), "metadata.name")
	}
	if err := modelcatalog.ValidatePreprocessor(p); err != nil {
		return managementError("InvalidPreprocessor", err.Error(), "spec")
	}
	for i, artifact := range p.Spec.Artifacts {
		field := fmt.Sprintf("spec.artifacts[%d].target", i)
		model, err := m.store.GetModel(ctx, artifact.Target.Model)
		if apierrors.IsNotFound(err) {
			return managementError("InvalidModelReference", "target ModelSpec is not registered", field+".model")
		}
		if err != nil {
			return err
		}
		if !model.DeletionTimestamp.IsZero() {
			return managementError("ModelDeleting", "target ModelSpec is being deleted", field+".model")
		}
		if !slices.ContainsFunc(model.Spec.Artifacts, func(a api.ModelArtifact) bool { return a.Name == artifact.Target.Artifact }) {
			return managementError("InvalidModelReference", "target ModelSpec has no such artifact", field+".artifact")
		}
	}
	return nil
}

func validateDataset(d *api.Dataset) error {
	if messages := validation.IsDNS1123Subdomain(d.Name); len(messages) != 0 {
		return managementError("InvalidDataset", fmt.Sprint(messages), "metadata.name")
	}
	if err := modelcatalog.ValidateDataset(d); err != nil {
		return managementError("InvalidDataset", err.Error(), "spec")
	}
	return nil
}

func (m *Manager) ListPreprocessors(ctx context.Context) (*api.PreprocessorList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.ListPreprocessors(ctx)
}

func (m *Manager) GetPreprocessor(ctx context.Context, name string) (*api.Preprocessor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.GetPreprocessor(ctx, name)
}

func (m *Manager) CreatePreprocessor(ctx context.Context, name string, spec api.PreprocessorSpec) (*api.Preprocessor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return nil, err
	}
	p := &api.Preprocessor{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
	if err := m.validatePreprocessor(ctx, p); err != nil {
		return nil, err
	}
	if err := m.store.CreatePreprocessor(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// UpdatePreprocessor starts a PreprocessorUpdate barrier for referencing
// Workloads once every dataset they name stays compatible (DP-10).
func (m *Manager) UpdatePreprocessor(ctx context.Context, name, resourceVersion string, spec api.PreprocessorSpec) (*api.Preprocessor, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return nil, false, err
	}
	p, err := m.store.GetPreprocessor(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if err := checkVersion(p.ResourceVersion, resourceVersion); err != nil {
		return p, false, err
	}
	proposed := p.DeepCopy()
	proposed.Spec = spec
	if err := m.validatePreprocessor(ctx, proposed); err != nil {
		return p, false, err
	}
	group, err := m.sharedGroup(ctx, p.UID, holdsPreprocessor(p), func(s api.WorkloadSpec) bool { return s.PreprocessorRef.Name == p.Name })
	if err != nil {
		return p, false, err
	}
	for _, w := range group {
		if w.Spec.DatasetRef == nil {
			continue
		}
		d, err := m.store.GetDataset(ctx, w.Spec.DatasetRef.Name)
		if err != nil {
			return p, false, err
		}
		if mismatched := modelcatalog.Compatible(d, proposed); len(mismatched) != 0 {
			return p, false, managementError("IncompatibleDataset", fmt.Sprintf("Workload %s dataset %s does not satisfy input fields %v", w.Name, d.Name, mismatched), "spec.input.fields")
		}
	}
	if reflect.DeepEqual(p.Spec, spec) {
		if m.settled(group, func(d *serving.Definition) bool { return d.Preprocessor.Generation == p.Generation }) {
			return p, false, nil
		}
	} else {
		if err := m.store.UpdatePreprocessor(ctx, proposed); err != nil {
			return p, false, err
		}
		p = proposed
	}
	if err := m.startSharedUpdate(ctx, operationPreprocessorUpdate, p.UID, p.Generation, group); err != nil {
		return p, false, err
	}
	return p, len(group) != 0, nil
}

func (m *Manager) DeletePreprocessor(ctx context.Context, name, resourceVersion string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return err
	}
	p, err := m.store.GetPreprocessor(ctx, name)
	if err != nil {
		return err
	}
	if err := checkVersion(p.ResourceVersion, resourceVersion); err != nil {
		return err
	}
	list, err := m.referenceWorkloads(ctx)
	if err != nil {
		return err
	}
	if refs := workloadNames(m.referencing(list, holdsPreprocessor(p))); len(refs) != 0 {
		return managementError("PreprocessorReferenced", fmt.Sprintf("Preprocessor is referenced by Workloads %v", refs), "metadata.name")
	}
	return m.store.DeletePreprocessor(ctx, p)
}

func (m *Manager) ListDatasets(ctx context.Context) (*api.DatasetList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.ListDatasets(ctx)
}

func (m *Manager) GetDataset(ctx context.Context, name string) (*api.Dataset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.GetDataset(ctx, name)
}

func (m *Manager) CreateDataset(ctx context.Context, name string, spec api.DatasetSpec) (*api.Dataset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return nil, err
	}
	d := &api.Dataset{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
	if err := validateDataset(d); err != nil {
		return nil, err
	}
	if err := m.store.CreateDataset(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (m *Manager) UpdateDataset(ctx context.Context, name, resourceVersion string, spec api.DatasetSpec) (*api.Dataset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return nil, err
	}
	d, err := m.store.GetDataset(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(d.ResourceVersion, resourceVersion); err != nil {
		return d, err
	}
	proposed := d.DeepCopy()
	proposed.Spec = spec
	if err := validateDataset(proposed); err != nil {
		return d, err
	}
	// DP-10: the Serving applied definition keeps the dataset identity; later
	// measurements freeze the new declaration, which must still serve every
	// preprocessor named together with it.
	list, err := m.store.ListWorkloads(ctx)
	if err != nil {
		return d, err
	}
	for _, w := range list.Items {
		if w.Spec.DatasetRef == nil || w.Spec.DatasetRef.Name != d.Name {
			continue
		}
		p, err := m.store.GetPreprocessor(ctx, w.Spec.PreprocessorRef.Name)
		if err != nil {
			return d, err
		}
		if mismatched := modelcatalog.Compatible(proposed, p); len(mismatched) != 0 {
			return d, managementError("IncompatibleDataset", fmt.Sprintf("Workload %s preprocessor %s input fields %v are not satisfied", w.Name, p.Name, mismatched), "spec.fields")
		}
	}
	if err := m.store.UpdateDataset(ctx, proposed); err != nil {
		return d, err
	}
	return proposed, nil
}

func (m *Manager) DeleteDataset(ctx context.Context, name, resourceVersion string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.requireReady(); err != nil {
		return err
	}
	d, err := m.store.GetDataset(ctx, name)
	if err != nil {
		return err
	}
	if err := checkVersion(d.ResourceVersion, resourceVersion); err != nil {
		return err
	}
	list, err := m.referenceWorkloads(ctx)
	if err != nil {
		return err
	}
	if refs := workloadNames(m.referencing(list, holdsDataset(d))); len(refs) != 0 {
		return managementError("DatasetReferenced", fmt.Sprintf("Dataset is referenced by Workloads %v", refs), "metadata.name")
	}
	return m.store.DeleteDataset(ctx, d)
}
