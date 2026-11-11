package management

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
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

type ManagerConfig struct {
	DrainTimeout time.Duration
	// Actor projects only the verified context identity at the composition root.
	Actor func(context.Context) (string, string)
	// OnWorkload projects a recorded change without I/O or reentering Manager.
	OnWorkload func(WorkloadView, bool)
}

type deletionReserver interface {
	ReserveDeletion(context.Context, types.UID) error
	ReleaseDeletion(types.UID)
}

type workloadStore interface {
	Client() client.Client
	GetWorkload(context.Context, string) (*api.Workload, error)
	ListWorkloads(context.Context) (*api.WorkloadList, error)
	CreateWorkload(context.Context, *api.Workload) error
	UpdateWorkload(context.Context, *api.Workload) error
	DeleteWorkload(context.Context, *api.Workload) error
	UpdateWorkloadStatus(context.Context, *api.Workload, api.WorkloadStatus) (*api.Workload, error)
	EnsureWorkloadFinalizer(context.Context, *api.Workload) error
	RemoveWorkloadFinalizer(context.Context, *api.Workload) error
	WorkloadDeleted(context.Context, string, types.UID) (bool, error)
	GetModel(context.Context, string) (*api.ModelSpec, error)
	ListModels(context.Context) (*api.ModelSpecList, error)
	CreateModel(context.Context, *api.ModelSpec) error
	UpdateModel(context.Context, *api.ModelSpec) error
	DeleteModel(context.Context, *api.ModelSpec) error
	GetPreprocessor(context.Context, string) (*api.Preprocessor, error)
	ListPreprocessors(context.Context) (*api.PreprocessorList, error)
	CreatePreprocessor(context.Context, *api.Preprocessor) error
	UpdatePreprocessor(context.Context, *api.Preprocessor) error
	DeletePreprocessor(context.Context, *api.Preprocessor) error
	GetDataset(context.Context, string) (*api.Dataset, error)
	ListDatasets(context.Context) (*api.DatasetList, error)
	CreateDataset(context.Context, *api.Dataset) error
	UpdateDataset(context.Context, *api.Dataset) error
	DeleteDataset(context.Context, *api.Dataset) error
}

type workloadObserver interface {
	Profiles() []serving.Profile
	CleanupWorkload(context.Context, types.UID) (bool, error)
}

// Manager is the only definition writer. It observes drain without holding this
// lock across a wait; Advance performs one finite observation/application pass.
// ponytail: one management lock and native list scans suit research-scale edits.
// Split locks only if management contention is measured.
type Manager struct {
	mu             sync.Mutex
	store          workloadStore
	runtime        *serving.Runtime
	observationLog *observation.ObservationLog
	observer       workloadObserver
	reservations   deletionReserver
	config         ManagerConfig
	initialized    bool
	// A failed status write must not let a subsequent poll resume the operation.
	// Only terminal failures are retained here; native status owns normal progress.
	failed map[types.UID]api.WorkloadStatus
	// This is only proof of a deletion already performed, never a workflow to
	// replay. The native object may disappear before its confirming GET succeeds.
	deletionChecks map[types.UID]*api.Workload
}

func NewManager(reservations deletionReserver, store workloadStore, runtime *serving.Runtime, observationLog *observation.ObservationLog, observer workloadObserver, config ManagerConfig) (*Manager, error) {
	if nilDependency(reservations) || nilDependency(store) || store.Client() == nil || runtime == nil || observationLog == nil || nilDependency(observer) || config.Actor == nil || config.DrainTimeout <= 0 {
		return nil, errors.New("native store, runtime, observation log, observer and positive drain timeout are required")
	}
	return &Manager{reservations: reservations, store: store, runtime: runtime, observationLog: observationLog, observer: observer, config: config,
		failed: map[types.UID]api.WorkloadStatus{}, deletionChecks: map[types.UID]*api.Workload{}}, nil
}

// Interfaces retain the concrete constructors' typed-nil rejection.
func nilDependency(value any) bool {
	v := reflect.ValueOf(value)
	return !v.IsValid() || v.Kind() == reflect.Pointer && v.IsNil()
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
		Admission:          api.WorkloadAdmission{Policy: api.AdmissionPolicy(d.Admission.Policy), MaxQueuedRequests: d.Admission.MaxQueuedRequests},
	}
	if d.RequestLifetime != nil {
		status.RequestLifetime = &metav1.Duration{Duration: *d.RequestLifetime}
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
		status.Admission = api.WorkloadAdmissionStatus{Allowed: state.Admission, Reason: state.Reason, ObservedAt: metav1.NewTime(state.ObservedAt)}
	} else {
		status.Admission = api.WorkloadAdmissionStatus{Reason: "AppliedDefinitionUnavailable", ObservedAt: metav1.Now()}
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
	actorUsername, actorUID := m.config.Actor(ctx)
	if op := m.status(w).Operation; op != nil {
		kind = op.Kind
		// Polling and restart must retain the original accepted-operation actor.
		actorUsername, actorUID = op.ActorUsername, op.ActorUID
	}
	event := serving.ManagementEvent{WorkloadUID: string(w.UID), Kind: kind, Phase: phase, Before: before, Target: target, After: after, At: at,
		ActorUsername: actorUsername, ActorUID: actorUID}
	if err != nil {
		event.Error = failureReason(err)
	}
	if err := m.observationLog.ManagementEvent(event); err != nil {
		return err
	}
	if m.config.OnWorkload != nil {
		m.config.OnWorkload(m.view(w), kind == operationDelete && phase == operationCompleted)
	}
	return nil
}

func (m *Manager) fail(ctx context.Context, w *api.Workload, cause error) error {
	_ = m.runtime.StopAdmission(string(w.UID), failureReason(cause))
	status := m.status(w)
	if status.Operation == nil {
		actorUsername, actorUID := m.config.Actor(ctx)
		status.Operation = &api.WorkloadOperation{Kind: "Update", TargetGeneration: w.Generation, AcceptedAt: metav1.Now(), ActorUsername: actorUsername, ActorUID: actorUID}
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
	status.Admission = api.WorkloadAdmissionStatus{Reason: failureReason(cause), ObservedAt: now}
	m.failed[w.UID] = status
	writeErr := m.persist(ctx, w, status)
	if writeErr == nil {
		delete(m.failed, w.UID)
	}
	observationLogErr := m.record(ctx, w, operationFailed, nil, nil, m.applied(w), now.Time, cause)
	return errors.Join(cause, writeErr, observationLogErr)
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

// catalogRefs holds the catalog objects one Workload spec names.
type catalogRefs struct {
	model        *api.ModelSpec
	preprocessor *api.Preprocessor
	dataset      *api.Dataset
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
	if err := ValidateModel(refs.model); err != nil {
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
			if _, unwritten := m.failed[w.UID]; unwritten || !m.observationLog.Health().Healthy {
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
	return Snapshot(w, refs.model, refs.preprocessor, refs.dataset)
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
			want, err = Snapshot(w, refs.model, refs.preprocessor, refs.dataset)
		}
		if err != nil || want.Identity() != d.Identity() || want.SLO != d.SLO || want.Admission != d.Admission || !reflect.DeepEqual(want.RequestLifetime, d.RequestLifetime) || !reflect.DeepEqual(want.Dataset, d.Dataset) {
			return managementError("AppliedStateUnconfirmed", "desired and recorded applied definitions differ", "status.appliedDefinition")
		}
		if err := m.persist(ctx, w, status); err != nil {
			return err
		}
		if _, err := m.runtime.ResumeAfterRestart(ctx, string(w.UID)); err != nil {
			return err
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
	d, err := Snapshot(w, refs.model, refs.preprocessor, refs.dataset)
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
	actorUsername, actorUID := m.config.Actor(ctx)
	status.Operation = &api.WorkloadOperation{Kind: "Update", Phase: "Draining", TargetGeneration: w.Generation, AcceptedAt: metav1.Now(), ActorUsername: actorUsername, ActorUID: actorUID}
	d, err := Snapshot(w, refs.model, refs.preprocessor, refs.dataset)
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
	// SLO, dataset, lifetime and admission changes keep the accepted execution and apply without drain (WM-16).
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
	if err := m.reservations.ReserveDeletion(ctx, w.UID); err != nil {
		return m.view(w), err
	}
	before := m.applied(w)
	if err := m.stop(w, "Deleting"); err != nil {
		m.reservations.ReleaseDeletion(w.UID)
		return m.view(w), err
	}
	if err := m.store.DeleteWorkload(ctx, w); err != nil {
		actorUsername, actorUID := m.config.Actor(ctx)
		status := w.Status
		status.Operation = &api.WorkloadOperation{Kind: operationDelete, Phase: "Draining", TargetGeneration: w.Generation, AcceptedAt: metav1.Now(), ActorUsername: actorUsername, ActorUID: actorUID}
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
	actorUsername, actorUID := m.config.Actor(ctx)
	status.Operation = &api.WorkloadOperation{Kind: operationDelete, Phase: "Draining", TargetGeneration: w.Generation, AcceptedAt: metav1.Now(), ActorUsername: actorUsername, ActorUID: actorUID}
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

func (m *Manager) drain(ctx context.Context, w *api.Workload) (bool, error) {
	if done, err := m.runtime.Drain(ctx, string(w.UID)); err != nil {
		return false, err
	} else if done {
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
	if op.Kind == operationDelete {
		if err := m.reservations.ReserveDeletion(ctx, w.UID); err != nil {
			return err
		}
	}
	if op.Phase == "Stopping" || op.Phase == "Draining" {
		done, err := m.drain(ctx, w)
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
		d, err := Snapshot(w, refs.model, refs.preprocessor, refs.dataset)
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
	m.reservations.ReleaseDeletion(w.UID)
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
		done, err := m.drain(ctx, w)
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
		if definitions[i], err = Snapshot(w, refs.model, refs.preprocessor, refs.dataset); err != nil {
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
				if stateErr == nil && state.Applied.UID == string(w.UID) && state.Reason == "RestartDrain" {
					_, stateErr = m.runtime.ResumeAfterRestart(ctx, string(w.UID))
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
	actorUsername, actorUID := m.config.Actor(ctx)
	statuses := make([]api.WorkloadStatus, len(group))
	for i, w := range group {
		delete(m.failed, w.UID)
		statuses[i] = *w.Status.DeepCopy()
		statuses[i].Operation = &api.WorkloadOperation{Kind: kind, Phase: "Draining", TargetGeneration: w.Generation,
			TargetCatalogUID: uid, TargetCatalogGeneration: generation, AcceptedAt: acceptedAt, ActorUsername: actorUsername, ActorUID: actorUID}
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
		d, err := Snapshot(w, refs.model, refs.preprocessor, refs.dataset)
		if err != nil {
			return fail(err)
		}
		if err := m.record(ctx, w, "Accepted", m.applied(w), d, nil, acceptedAt.Time, nil); err != nil {
			return fail(err)
		}
	}
	return nil
}
