package management

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	"github.com/lab-paper-code/chill/internal/serving"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

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
	if err := ValidateModel(model); err != nil {
		return nil, err
	}
	if err := m.store.CreateModel(ctx, model); err != nil {
		return nil, err
	}
	return model, nil
}

// Reference helpers require the caller to hold m.mu.

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
	if err := ValidateModel(proposed); err != nil {
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
	// preprocessor paired with it, desired or still applied during a drain.
	list, err := m.referenceWorkloads(ctx)
	if err != nil {
		return d, err
	}
	checked := map[string]bool{}
	check := func(w *api.Workload, p *api.Preprocessor) error {
		key := fmt.Sprintf("%s/%d", p.UID, p.Generation)
		if checked[key] {
			return nil
		}
		checked[key] = true
		if mismatched := modelcatalog.Compatible(proposed, p); len(mismatched) != 0 {
			return managementError("IncompatibleDataset", fmt.Sprintf("Workload %s preprocessor %s input fields %v are not satisfied", w.Name, p.Name, mismatched), "spec.fields")
		}
		return nil
	}
	for _, w := range m.referencing(list, holdsDataset(d)) {
		if w.Spec.DatasetRef != nil && w.Spec.DatasetRef.Name == d.Name {
			p, err := m.store.GetPreprocessor(ctx, w.Spec.PreprocessorRef.Name)
			if err != nil {
				return d, err
			}
			if err := check(w, p); err != nil {
				return d, err
			}
		}
		if a := m.applied(w); a != nil && a.Dataset != nil && a.Dataset.UID == string(d.UID) {
			p := &api.Preprocessor{ObjectMeta: metav1.ObjectMeta{Name: a.Preprocessor.Name, UID: types.UID(a.Preprocessor.UID), Generation: a.Preprocessor.Generation}}
			for _, field := range a.Preprocessor.Input {
				p.Spec.Input.Fields = append(p.Spec.Input.Fields, api.RawField{Name: field.Name, Format: field.Format, MaxBytes: field.MaxBytes})
			}
			if err := check(w, p); err != nil {
				return d, err
			}
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
