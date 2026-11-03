package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type LoadService struct {
	client    client.Client
	namespace string
	evidence  evidencestore.Statter
}

func loadError(code, message string) error { return &serving.Error{Code: code, Message: message} }
func (s *LoadService) inUse(ctx context.Context, name string) ([]string, error) {
	var runs api.EvaluationRunList
	if err := s.client.List(ctx, &runs, client.InNamespace(s.namespace)); err != nil {
		return nil, err
	}
	names := []string{}
	for _, run := range runs.Items {
		if meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionPublished) {
			continue
		}
		for _, stage := range run.Spec.Stages {
			if stage.LoadScenarioRef.Name == name {
				names = append(names, run.Name)
				break
			}
		}
	}
	sort.Strings(names)
	return names, nil
}
func (s *LoadService) view(ctx context.Context, obj *api.LoadScenario) (LoadScenarioView, error) {
	names, err := s.inUse(ctx, obj.Name)
	return LoadScenarioView{
		Name: obj.Name, UID: string(obj.UID), Spec: obj.Spec, InUse: names, CreatedAt: obj.CreationTimestamp,
	}, err
}
func (s *LoadService) List(ctx context.Context) ([]LoadScenarioView, error) {
	var objects api.LoadScenarioList
	if err := s.client.List(ctx, &objects, client.InNamespace(s.namespace)); err != nil {
		return nil, err
	}
	result := make([]LoadScenarioView, 0, len(objects.Items))
	for i := range objects.Items {
		v, err := s.view(ctx, &objects.Items[i])
		if err != nil {
			return result, err
		}
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
func (s *LoadService) Get(ctx context.Context, name string) (LoadScenarioView, error) {
	var obj api.LoadScenario
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &obj); err != nil {
		return LoadScenarioView{}, err
	}
	return s.view(ctx, &obj)
}
func (s *LoadService) Preflight(
	ctx context.Context, request LoadScenarioRequest,
) (LoadScenarioPreflight, error) {
	result := LoadScenarioPreflight{
		Checks: []EvaluationCheck{}, LiveWorkloads: map[string]LiveWorkload{},
	}
	add := func(id string, err error) {
		check := EvaluationCheck{ID: id, Passed: err == nil}
		if err != nil {
			check.Reason = err.Error()
		}
		result.Checks = append(result.Checks, check)
	}
	var nameErr error
	if len(validation.IsDNS1123Subdomain(request.Name)) != 0 {
		nameErr = fmt.Errorf("valid name required")
	}
	add("name", nameErr)
	add("spec", request.Spec.Validate())
	for _, ref := range request.Spec.Workloads {
		var obj api.Workload
		err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: ref.Name}, &obj)
		if err == nil {
			result.LiveWorkloads[ref.Name] = LiveWorkload{UID: string(obj.UID), Generation: obj.Generation}
			if obj.UID != ref.UID || obj.Generation != ref.Generation {
				err = fmt.Errorf("workload identity or generation differs")
			}
		}
		add("workload:"+ref.Name, err)
	}
	for workload, ref := range request.Spec.DatasetRefs {
		var dataset api.Dataset
		err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: ref.Name}, &dataset)
		if err == nil && dataset.UID != ref.UID {
			err = fmt.Errorf("dataset UID differs")
		}
		add("dataset:"+workload, err)
	}
	for _, ref := range request.Spec.TraceRefs {
		object, err := s.evidence.Stat(ctx, evidencestore.ObjectLocation{Store: ref.Store, Key: ref.Key})
		if err == nil && object.Size != ref.Size {
			err = fmt.Errorf("evidence size differs: live %d, requested %d", object.Size, ref.Size)
		}
		add("evidence:"+ref.Key, err)
	}
	return result, nil
}
func (s *LoadService) checked(ctx context.Context, request LoadScenarioRequest) error {
	result, err := s.Preflight(ctx, request)
	if err != nil {
		return err
	}
	for _, check := range result.Checks {
		if !check.Passed {
			return loadError("InvalidInput", check.ID+": "+check.Reason)
		}
	}
	return nil
}
func (s *LoadService) createObject(
	ctx context.Context, request LoadScenarioRequest, actor string,
) (EvaluationIdentity, error) {
	obj := api.LoadScenario{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: s.namespace, Name: request.Name,
			Annotations: map[string]string{"edge.dacs.io/requested-by": actor},
		},
		Spec: request.Spec,
	}
	err := s.client.Create(ctx, &obj)
	return EvaluationIdentity{Name: obj.Name, UID: string(obj.UID)}, err
}
func (s *LoadService) Create(
	ctx context.Context, request LoadScenarioRequest, actor string,
) (EvaluationIdentity, error) {
	if err := s.checked(ctx, request); err != nil {
		return EvaluationIdentity{}, err
	}
	return s.createObject(ctx, request, actor)
}
func (s *LoadService) removable(ctx context.Context, name, uid string) (*api.LoadScenario, error) {
	if uid == "" {
		return nil, loadError("InvalidInput", "uid is required")
	}
	var obj api.LoadScenario
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &obj); err != nil {
		return nil, err
	}
	if string(obj.UID) != uid {
		return nil, loadError("Conflict", "LoadScenario UID changed")
	}
	names, err := s.inUse(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(names) > 0 {
		return nil, loadError("Conflict", "LoadScenario in use: "+strings.Join(names, ", "))
	}
	return &obj, nil
}
func (s *LoadService) Delete(ctx context.Context, name, uid string) (EvaluationIdentity, error) {
	identity := EvaluationIdentity{Name: name, UID: uid}
	obj, err := s.removable(ctx, name, uid)
	if err != nil {
		return identity, err
	}
	pinned := types.UID(uid)
	return identity, s.client.Delete(ctx, obj, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pinned}})
}
func (s *LoadService) Replace(
	ctx context.Context, name, uid string, request LoadScenarioRequest, actor string,
) (EvaluationIdentity, error) {
	if request.Name != "" && request.Name != name {
		return EvaluationIdentity{}, loadError("InvalidInput", "body name must match path name")
	}
	request.Name = name
	if err := s.checked(ctx, request); err != nil {
		return EvaluationIdentity{}, err
	}
	if _, err := s.Delete(ctx, name, uid); err != nil {
		return EvaluationIdentity{}, err
	}
	err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true,
		func(ctx context.Context) (bool, error) {
			var obj api.LoadScenario
			err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &obj)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
	if err != nil {
		return EvaluationIdentity{}, fmt.Errorf("이전 객체 삭제됨; replacement wait: %w", err)
	}
	identity, err := s.createObject(ctx, request, actor)
	if err != nil {
		return identity, fmt.Errorf("이전 객체 삭제됨; recreate: %w", err)
	}
	return identity, nil
}
func (s *LoadService) Inputs(ctx context.Context) ([]EvaluationInput, error) {
	var result []EvaluationInput
	var scenarios api.LoadScenarioList
	if err := s.client.List(ctx, &scenarios, client.InNamespace(s.namespace)); err != nil {
		return result, err
	}
	seen := map[api.EvaluationEvidenceReference]bool{}
	for _, scenario := range scenarios.Items {
		for workload, ref := range scenario.Spec.TraceRefs {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			result = append(result, EvaluationInput{
				Store: ref.Store, Key: ref.Key, Size: ref.Size, Kind: "trace",
				Set: scenario.Name, Name: scenario.Name + " / " + workload,
			})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func NewLoadService(native client.Client, namespace string, evidence evidencestore.Statter) *LoadService {
	return &LoadService{client: native, namespace: namespace, evidence: evidence}
}
