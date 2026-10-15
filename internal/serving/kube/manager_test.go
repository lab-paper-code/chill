package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The fake native client does not allocate registration UIDs or generations.
// Supply only those API-server semantics, not lifecycle/runtime behavior.
type managerAPI struct {
	client.Client
	next int
}

func (c *managerAPI) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.next++
	obj.SetUID(types.UID(fmt.Sprintf("native-%d", c.next)))
	obj.SetGeneration(1)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *managerAPI) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	previous := obj.DeepCopyObject().(client.Object)
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), previous); err != nil {
		return err
	}
	changed := false
	switch current := obj.(type) {
	case *api.Workload:
		changed = !reflect.DeepEqual(current.Spec, previous.(*api.Workload).Spec)
	case *api.ModelSpec:
		changed = !reflect.DeepEqual(current.Spec, previous.(*api.ModelSpec).Spec)
	case *api.Preprocessor:
		changed = !reflect.DeepEqual(current.Spec, previous.(*api.Preprocessor).Spec)
	case *api.Dataset:
		changed = !reflect.DeepEqual(current.Spec, previous.(*api.Dataset).Spec)
	}
	obj.SetGeneration(previous.GetGeneration())
	if changed {
		obj.SetGeneration(obj.GetGeneration() + 1)
	}
	return c.Client.Update(ctx, obj, opts...)
}

type managerArchive struct{ directory string }

func (a managerArchive) PutImmutable(_ context.Context, identity evidencestore.ObjectIdentity, input io.Reader) (evidencestore.Receipt, error) {
	name := filepath.Join(a.directory, identity.Key)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	outcome := evidencestore.CreateOutcomeCreated
	if errors.Is(err, os.ErrExist) {
		outcome = evidencestore.CreateOutcomeExisting
	} else if err != nil {
		return evidencestore.Receipt{}, err
	} else {
		n, copyErr := io.Copy(file, input)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n != identity.Size {
			_ = os.Remove(name)
			return evidencestore.Receipt{}, errors.Join(copyErr, closeErr, errors.New("incomplete immutable archive"))
		}
	}
	return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome}, nil
}

func newManagerHarness(t *testing.T) (*Manager, serving.JournalConfig) {
	t.Helper()
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	store := &Store{client: &managerAPI{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Workload{}, &corev1.Pod{}).Build()}}
	config := serving.JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: managerArchive{t.TempDir()}}
	journal, err := serving.NewJournal(config)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 8, Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewObserver(store, kubefake.NewSimpleClientset(), rt, &firstfloor.Client{HTTPClient: &http.Client{Timeout: time.Second}}, ObserverConfig{Namespace: "chill-system"})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, rt, journal, observer, ManagerConfig{DrainTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close(); _ = journal.Close(ctx) })
	return manager, config
}

func managerModelSpec() api.ModelSpecSpec {
	return api.ModelSpecSpec{
		Artifacts: []api.ModelArtifact{{Name: "onnx", Format: "onnx", Source: api.ModelArtifactSource{Image: "model@sha256:immutable", Architecture: "amd64", Files: []string{"/model.onnx"}}}},
		ExecutionPaths: []api.ModelExecutionPath{{Name: "native", Artifact: "onnx", Runtime: api.ModelRuntimeRequirement{Family: "firstfloor", Backend: "ort-cpu",
			Settings: &api.ModelRuntimeSettings{Firstfloor: &api.FirstfloorRuntimeSettings{MaxBatchSize: 4, MaxQueueSize: 8}}}}},
	}
}

func managerPreprocessorSpec() api.PreprocessorSpec {
	return api.PreprocessorSpec{
		Input:     api.RawInputContract{Fields: []api.RawField{{Name: "image", Format: "JPEG", MaxBytes: 1024}}},
		Output:    api.RawOutputContract{Fields: []api.RawOutputField{{Name: "label", Format: "UTF8"}}},
		Artifacts: []api.PreprocessorArtifact{{Target: api.PreprocessorTarget{Model: "model", Artifact: "onnx"}, Source: api.PreprocessorArtifactSource{Image: "pre@sha256:immutable", Architecture: "amd64", Path: "/artifact/pre"}}},
	}
}

func managerDatasetSpec() api.DatasetSpec {
	return api.DatasetSpec{Source: api.DatasetSource{Image: "data@sha256:immutable", Path: "/artifact/data"},
		Fields: []api.RawField{{Name: "image", Format: "JPEG", MaxBytes: 512}, {Name: "question", Format: "UTF8", MaxBytes: 64}}}
}

// createManagerCatalog registers the model, preprocessor and dataset managerWorkloadSpec names.
func createManagerCatalog(t *testing.T, m *Manager) *api.ModelSpec {
	t.Helper()
	ctx := context.Background()
	model, err := m.CreateModel(ctx, "model", managerModelSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreatePreprocessor(ctx, "pre", managerPreprocessorSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateDataset(ctx, "data", managerDatasetSpec()); err != nil {
		t.Fatal(err)
	}
	return model
}

func managerWorkloadSpec() api.WorkloadSpec {
	var spec api.WorkloadSpec
	spec.ModelRef.Name = "model"
	spec.PreprocessorRef.Name = "pre"
	spec.DatasetRef = &api.CatalogReference{Name: "data"}
	spec.SLO.Percentile = 99
	spec.SLO.TargetLatency.Duration = time.Second
	return spec
}

func managerProfile(d *serving.Definition) serving.Profile {
	path := d.Model.Paths[0]
	profile := serving.Profile{ID: "measured", Condition: derivation.CurvePlanSpec{LogicalModel: d.Model.Name, RuntimeFamily: path.RuntimeFamily, Backend: path.Backend, Firstfloor: path.Firstfloor,
		Artifact:     derivation.CurveArtifactContract{ExecutionPath: path.Name, Name: path.Artifact, Files: path.Files, Image: path.Image},
		Preprocessor: derivation.CurvePreprocessorContract{Name: d.Preprocessor.Name, Image: path.Preprocessor.Image, Architecture: path.Preprocessor.Architecture, Path: path.Preprocessor.Path, MaxNewTokens: int(d.Preprocessor.MaxNewTokens)}}}
	for _, field := range d.Preprocessor.Input {
		profile.Condition.Preprocessor.Inputs = append(profile.Condition.Preprocessor.Inputs, field.Name)
	}
	for _, field := range d.Preprocessor.Output {
		profile.Condition.Preprocessor.Outputs = append(profile.Condition.Preprocessor.Outputs, field.Name)
	}
	if d.Dataset != nil {
		profile.Condition.ActiveProtocol.Dataset = &derivation.CurveDatasetContract{Name: d.Dataset.Name, UID: d.Dataset.UID}
	}
	return profile
}

// managerSample is a raw sample satisfying managerPreprocessorSpec.
func managerSample(t *testing.T) *serving.InferenceInput {
	t.Helper()
	var picture bytes.Buffer
	if err := jpeg.Encode(&picture, image.NewGray(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	return &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "image", Datatype: "BYTES", Data: picture.Bytes()}}}
}

func startManagerCalls(t *testing.T, m *Manager, profile serving.Profile, names ...string) ([]serving.Receipt, []*serving.Call, chan struct{}, map[string]serving.Dispatch) {
	t.Helper()
	m.observer.SetProfiles([]serving.Profile{profile})
	m.runtime.SetProfiles([]serving.Profile{profile})
	started := make(chan serving.Dispatch, len(names))
	finish := make(chan struct{}, len(names))
	if err := m.runtime.InstallInstances([]serving.ReadyInstance{{State: serving.InstanceState{ID: "pod/container", Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Pool: "pool", Profile: profile.ID, Ready: true}, Profile: &profile, Weight: 10000, Capacity: 2,
		Execute: func(_ context.Context, d serving.Dispatch) (serving.BackendResult, error) {
			started <- d
			<-finish
			return serving.BackendResult{StatusCode: 200, Finished: true}, nil
		}}}); err != nil {
		t.Fatal(err)
	}
	input := managerSample(t)
	receipts := []serving.Receipt{}
	calls := []*serving.Call{}
	for _, name := range names {
		r, err := m.runtime.Receive(name, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		call, err := m.runtime.Accept(r, input)
		if err != nil {
			t.Fatal(err)
		}
		receipts, calls = append(receipts, r), append(calls, call)
	}
	dispatched := map[string]serving.Dispatch{}
	for range calls {
		select {
		case d := <-started:
			dispatched[d.Definition.Name] = d
		case <-time.After(time.Second):
			t.Fatal("actual dispatch did not start")
		}
	}
	return receipts, calls, finish, dispatched
}

func completeManagerResponses(t *testing.T, m *Manager, receipts []serving.Receipt, calls []*serving.Call) {
	t.Helper()
	for i, call := range calls {
		wait, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := call.Wait(wait)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.runtime.Response(receipts[i], "completed", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagerRegistrationIdentity(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{username: "registrar", uid: "registrar-uid"})
	m, _ := newManagerHarness(t)
	model := createManagerCatalog(t, m)
	first, err := m.CreateWorkload(ctx, "first", managerWorkloadSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateWorkload(ctx, "second", managerWorkloadSpec()); err != nil {
		t.Fatal(err)
	}
	if first.Admission || first.Readiness != "Waiting" || first.Reason != "MissingEvidence" || first.Applied == nil {
		t.Fatalf("valid unmeasured registration = %+v", first)
	}
	if _, err := m.CreateWorkload(ctx, "first", managerWorkloadSpec()); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("duplicate registration = %v", err)
	}
	if err := m.DeleteModel(ctx, "model", model.ResourceVersion); err == nil {
		t.Fatal("deleted a desired/applied model reference")
	}
	receipts, calls, finish, _ := startManagerCalls(t, m, managerProfile(first.Applied), "first")
	finish <- struct{}{}
	completeManagerResponses(t, m, receipts, calls)
	first, err = m.GetWorkload(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	oldUID := first.Desired.UID
	deleteCtx := WithPrincipal(context.Background(), Principal{username: "deleter", uid: "deleter-uid"})
	if _, err := m.DeleteWorkload(deleteCtx, "first", first.Desired.ResourceVersion); err != nil {
		t.Fatal(err)
	}
	if err := m.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetWorkload(ctx, "first"); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted registration remains visible: %v", err)
	}
	second, err := m.GetWorkload(ctx, "second")
	if err != nil || second.Applied.Model.Generation != model.Generation {
		t.Fatal("deletion disturbed the shared surviving workload")
	}
	first, err = m.CreateWorkload(ctx, "first", managerWorkloadSpec())
	if err != nil || first.Desired.UID == oldUID {
		t.Fatal("same-name registration reused old identity")
	}
	if _, _, err := m.UpdateWorkload(ctx, "first", "stale", first.Desired.Spec); err == nil {
		t.Fatal("accepted stale precondition")
	}
	observed, err := m.journal.Query(string(oldUID), time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil || len(observed.Definitions) == 0 || len(observed.Management) == 0 {
		t.Fatal("deletion lost immutable request/management interpretation")
	}
	for _, event := range observed.Management {
		wantName, wantUID := "registrar", "registrar-uid"
		if event.Kind == "Delete" {
			wantName, wantUID = "deleter", "deleter-uid"
		}
		if event.ActorUsername != wantName || event.ActorUID != wantUID {
			t.Fatalf("original operation actor lost: %+v", event)
		}
	}
}

func TestManagerSLOUpdatePreservesAcceptedSnapshot(t *testing.T) {
	ctx := context.Background()
	m, _ := newManagerHarness(t)
	createManagerCatalog(t, m)
	first, err := m.CreateWorkload(ctx, "first", managerWorkloadSpec())
	if err != nil {
		t.Fatal(err)
	}
	receipts, calls, finish, dispatched := startManagerCalls(t, m, managerProfile(first.Applied), "first")
	first, err = m.GetWorkload(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	slo := first.Desired.Spec
	slo.SLO.TargetLatency.Duration = 2 * time.Second
	first, accepted, err := m.UpdateWorkload(ctx, "first", first.Desired.ResourceVersion, slo)
	if err != nil || accepted {
		t.Fatalf("SLO-only update = %v, %v", accepted, err)
	}
	if dispatched["first"].Definition.SLO.TargetLatency != time.Second || first.Applied.SLO.TargetLatency != 2*time.Second || first.Operation.Phase != operationCompleted || first.Operation.AppliedAt == nil {
		t.Fatal("SLO replacement changed the accepted snapshot or was not actually completed")
	}
	finish <- struct{}{}
	completeManagerResponses(t, m, receipts, calls)
}

func TestManagerSharedModelBarrier(t *testing.T) {
	ctx := context.Background()
	m, _ := newManagerHarness(t)
	model := createManagerCatalog(t, m)
	first, err := m.CreateWorkload(ctx, "first", managerWorkloadSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateWorkload(ctx, "second", managerWorkloadSpec()); err != nil {
		t.Fatal(err)
	}
	receipts, calls, finish, _ := startManagerCalls(t, m, managerProfile(first.Applied), "first", "second")
	changed := model.Spec
	changed.Artifacts = append([]api.ModelArtifact(nil), model.Spec.Artifacts...)
	changed.Artifacts[0].Source.Image = "new@sha256:immutable"
	updateCtx := WithPrincipal(context.Background(), Principal{username: "model-editor", uid: "model-editor-uid"})
	model, accepted, err := m.UpdateModel(updateCtx, "model", model.ResourceVersion, changed)
	if err != nil || !accepted {
		t.Fatalf("shared update = %v, %v", accepted, err)
	}
	for _, name := range []string{"first", "second"} {
		view, err := m.GetWorkload(ctx, name)
		if err != nil || view.Admission || view.Applied.Model.Generation == model.Generation || view.Operation.TargetCatalogUID != model.UID {
			t.Fatalf("old shared barrier %s = %+v, %v", name, view, err)
		}
		if _, _, err := m.UpdateWorkload(ctx, name, view.Desired.ResourceVersion, view.Desired.Spec); err == nil {
			t.Fatal("competing workload update was accepted")
		}
	}
	if _, err := m.CreateWorkload(ctx, "during-barrier", managerWorkloadSpec()); err == nil {
		t.Fatal("registration joined an active model barrier")
	}
	finish <- struct{}{}
	// One executor remains active, and response records still need confirmation.
	// Neither member may publish before both native and response drain are proven.
	for {
		var completed int
		for _, name := range []string{"first", "second"} {
			state, _ := m.runtime.State(name)
			if state.Assigned == 0 {
				completed++
			}
		}
		if completed == 1 {
			break
		}
		select {
		case <-m.runtime.Changed():
		case <-time.After(time.Second):
			t.Fatal("first actual completion was not observed")
		}
	}
	if err := m.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		state, _ := m.runtime.State(name)
		if state.Applied.Model.Generation == model.Generation {
			t.Fatal("published before every group member drained")
		}
	}
	finish <- struct{}{}
	completeManagerResponses(t, m, receipts, calls)
	if err := m.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		view, err := m.GetWorkload(ctx, name)
		if err != nil || view.Applied.Model.Generation != model.Generation || view.Operation.Phase != operationCompleted || view.Reason != "MissingEvidence" {
			t.Fatalf("new shared definition %s = %+v, %v", name, view, err)
		}
		if view.Operation.ActorUsername != "model-editor" || view.Operation.ActorUID != "model-editor-uid" {
			t.Fatalf("durable shared update actor lost: %+v", view.Operation)
		}
		assertJournalActor(t, m.journal, string(view.Desired.UID), operationModelUpdate, "model-editor", "model-editor-uid")
	}
}

func TestManagerInterruptedOperationIsTerminalUntilExplicitPut(t *testing.T) {
	ctx := context.Background()
	m, config := newManagerHarness(t)
	model := createManagerCatalog(t, m)
	view, err := m.CreateWorkload(ctx, "service", managerWorkloadSpec())
	if err != nil {
		t.Fatal(err)
	}
	old := view.Applied
	changed := model.Spec
	changed.Artifacts = append([]api.ModelArtifact(nil), model.Spec.Artifacts...)
	changed.Artifacts[0].Source.Files = []string{"/changed.onnx"}
	originalCtx := WithPrincipal(context.Background(), Principal{username: "original-editor", uid: "original-uid"})
	model, _, err = m.UpdateModel(originalCtx, "model", model.ResourceVersion, changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.journal.Close(ctx); err != nil {
		t.Fatal(err)
	}
	journal, err := serving.NewJournal(config)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 8, Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewObserver(m.store, kubefake.NewSimpleClientset(), rt, &firstfloor.Client{HTTPClient: &http.Client{Timeout: time.Second}}, ObserverConfig{Namespace: "chill-system"})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(m.store, rt, journal, observer, ManagerConfig{DrainTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close(); _ = journal.Close(ctx) })
	if err := restarted.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = restarted.GetWorkload(ctx, "service")
	if err != nil || view.Admission || view.Operation.Phase != operationFailed || view.Applied.Identity() != old.Identity() {
		t.Fatalf("interrupted operation auto-resumed: %+v %v", view, err)
	}
	if view.Operation.ActorUsername != "original-editor" || view.Operation.ActorUID != "original-uid" {
		t.Fatalf("restart lost accepted actor: %+v", view.Operation)
	}
	assertJournalActor(t, journal, string(view.Desired.UID), operationModelUpdate, "original-editor", "original-uid")
	_, accepted, err := restarted.UpdateWorkload(ctx, "service", view.Desired.ResourceVersion, view.Desired.Spec)
	if err != nil || !accepted {
		t.Fatalf("explicit new PUT = %v %v", accepted, err)
	}
	if err := restarted.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = restarted.GetWorkload(ctx, "service")
	if err != nil || view.Operation.Phase != operationCompleted || view.Applied.Model.Generation != model.Generation {
		t.Fatalf("explicit replacement did not apply: %+v %v", view, err)
	}
}

func assertJournalActor(t *testing.T, journal *serving.Journal, uid, kind, username, actorUID string) {
	t.Helper()
	observed, err := journal.Query(uid, time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range observed.Management {
		if event.Kind == kind && (event.ActorUsername != username || event.ActorUID != actorUID) {
			t.Fatalf("original operation actor changed: %+v", event)
		}
	}
}

func TestManagerPreprocessorTargetsProtectModelArtifacts(t *testing.T) {
	ctx := context.Background()
	m, _ := newManagerHarness(t)
	model, err := m.CreateModel(ctx, "model", managerModelSpec())
	if err != nil {
		t.Fatal(err)
	}
	spec := managerPreprocessorSpec()
	spec.Artifacts[0].Target.Artifact = "missing"
	if _, err := m.CreatePreprocessor(ctx, "pre", spec); managerCode(err) != "InvalidModelReference" {
		t.Fatalf("unregistered target artifact = %v", err)
	}
	if _, err := m.CreatePreprocessor(ctx, "pre", managerPreprocessorSpec()); err != nil {
		t.Fatal(err)
	}
	renamed := managerModelSpec()
	renamed.Artifacts[0].Name = "renamed"
	renamed.ExecutionPaths[0].Artifact = renamed.Artifacts[0].Name
	if _, _, err := m.UpdateModel(ctx, "model", model.ResourceVersion, renamed); managerCode(err) != "ModelArtifactReferenced" {
		t.Fatalf("removed targeted artifact = %v", err)
	}
	if err := m.DeleteModel(ctx, "model", model.ResourceVersion); managerCode(err) != "ModelReferenced" {
		t.Fatalf("deleted targeted model = %v", err)
	}
	pre, err := m.GetPreprocessor(ctx, "pre")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeletePreprocessor(ctx, "pre", pre.ResourceVersion); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteModel(ctx, "model", model.ResourceVersion); err != nil {
		t.Fatalf("untargeted model deletion = %v", err)
	}
}

func managerCode(err error) string {
	var domain *serving.Error
	if errors.As(err, &domain) {
		return domain.Code
	}
	return fmt.Sprint(err)
}

func TestManagerCatalogReferences(t *testing.T) {
	const missingDataset, incompatible = "MissingDataset", "IncompatibleDataset"
	ctx := context.Background()
	m, _ := newManagerHarness(t)
	createManagerCatalog(t, m)
	spec := managerWorkloadSpec()
	spec.DatasetRef = nil
	view, err := m.CreateWorkload(ctx, "service", spec)
	if err != nil || view.Admission || view.Readiness != "Waiting" || view.Reason != missingDataset {
		t.Fatalf("registration without dataset = %+v, %v", view, err)
	}
	receipt, err := m.runtime.Receive("service", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	input := managerSample(t)
	if _, err := m.runtime.Accept(receipt, input); managerCode(err) != missingDataset {
		t.Fatalf("request without dataset = %v", err)
	}
	narrow := managerDatasetSpec()
	narrow.Fields[0].MaxBytes = 4096
	if _, err := m.CreateDataset(ctx, "narrow", narrow); err != nil {
		t.Fatal(err)
	}
	spec.DatasetRef = &api.CatalogReference{Name: "narrow"}
	if _, _, err := m.UpdateWorkload(ctx, "service", view.Desired.ResourceVersion, spec); managerCode(err) != incompatible {
		t.Fatalf("incompatible dataset reference = %v", err)
	}
	spec.DatasetRef = &api.CatalogReference{Name: "data"}
	view, accepted, err := m.UpdateWorkload(ctx, "service", view.Desired.ResourceVersion, spec)
	if err != nil || accepted || view.Reason == missingDataset || view.Applied.Dataset == nil || view.Applied.Dataset.Name != "data" {
		t.Fatalf("dataset reference without drain = %+v, %v, %v", view, accepted, err)
	}
	dataset, err := m.GetDataset(ctx, "data")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateDataset(ctx, "data", dataset.ResourceVersion, narrow); managerCode(err) != incompatible {
		t.Fatalf("dataset update breaking a referenced pair = %v", err)
	}
	if err := m.DeleteDataset(ctx, "data", dataset.ResourceVersion); managerCode(err) != "DatasetReferenced" {
		t.Fatalf("referenced dataset deletion = %v", err)
	}
	pre, err := m.GetPreprocessor(ctx, "pre")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeletePreprocessor(ctx, "pre", pre.ResourceVersion); managerCode(err) != "PreprocessorReferenced" {
		t.Fatalf("referenced preprocessor deletion = %v", err)
	}
	wider := managerPreprocessorSpec()
	wider.Input.Fields[0].MaxBytes = 256
	if _, _, err := m.UpdatePreprocessor(ctx, "pre", pre.ResourceVersion, wider); managerCode(err) != incompatible {
		t.Fatalf("preprocessor update breaking a referenced pair = %v", err)
	}
}

func TestManagerPreprocessorUpdateBarrier(t *testing.T) {
	ctx := context.Background()
	m, _ := newManagerHarness(t)
	createManagerCatalog(t, m)
	first, err := m.CreateWorkload(ctx, "first", managerWorkloadSpec())
	if err != nil {
		t.Fatal(err)
	}
	receipts, calls, finish, dispatched := startManagerCalls(t, m, managerProfile(first.Applied), "first")
	pre, err := m.GetPreprocessor(ctx, "pre")
	if err != nil {
		t.Fatal(err)
	}
	changed := managerPreprocessorSpec()
	changed.Artifacts[0].Source.Path = "/artifact/changed"
	changed.Input.Fields[0].MaxBytes = 4096
	pre, accepted, err := m.UpdatePreprocessor(ctx, "pre", pre.ResourceVersion, changed)
	if err != nil || !accepted {
		t.Fatalf("referenced preprocessor update = %v, %v", accepted, err)
	}
	view, err := m.GetWorkload(ctx, "first")
	if err != nil || view.Admission || view.Operation.Kind != operationPreprocessorUpdate || view.Operation.TargetCatalogUID != pre.UID {
		t.Fatalf("preprocessor barrier = %+v, %v", view, err)
	}
	if _, err := m.CreateWorkload(ctx, "during-barrier", managerWorkloadSpec()); managerCode(err) != "OperationConflict" {
		t.Fatalf("registration joined an active preprocessor barrier: %v", err)
	}
	if err := m.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	if state, _ := m.runtime.State("first"); state.Applied.Preprocessor.Generation == pre.Generation {
		t.Fatal("published before accepted requests drained")
	}
	// The draining workload still applies the old 1024-byte contract, so a dataset
	// that only the new preprocessor accepts must wait for the barrier.
	larger := managerDatasetSpec()
	larger.Fields[0].MaxBytes = 2048
	dataset, err := m.GetDataset(ctx, "data")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateDataset(ctx, "data", dataset.ResourceVersion, larger); managerCode(err) != "IncompatibleDataset" {
		t.Fatalf("dataset update breaking the applied preprocessor = %v", err)
	}
	finish <- struct{}{}
	completeManagerResponses(t, m, receipts, calls)
	if err := m.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = m.GetWorkload(ctx, "first")
	if err != nil || view.Operation.Phase != operationCompleted || view.Applied.Preprocessor.Generation != pre.Generation || view.Applied.Model.Paths[0].Preprocessor.Path != "/artifact/changed" {
		t.Fatalf("new preprocessor definition = %+v, %v", view, err)
	}
	if dispatched["first"].Definition.Preprocessor.Generation == pre.Generation {
		t.Fatal("accepted request lost its preprocessor definition")
	}
	if _, err := m.UpdateDataset(ctx, "data", dataset.ResourceVersion, larger); err != nil {
		t.Fatalf("dataset update after the new preprocessor applied = %v", err)
	}
	if view, err = m.GetWorkload(ctx, "first"); err != nil || view.Operation.Phase != operationCompleted || view.Operation.Kind != operationPreprocessorUpdate {
		t.Fatalf("dataset update started an operation: %+v, %v", view.Operation, err)
	}
}
