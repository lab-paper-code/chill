package main

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type evaluationLoadService struct {
	client    client.Client
	namespace string
	evidence  evidencestore.Statter
	lister    evidencestore.Lister
}

func loadError(code, message string) error { return &serving.Error{Code: code, Message: message} }
func (s *evaluationLoadService) inUse(ctx context.Context, name string) ([]string, error) {
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
func (s *evaluationLoadService) view(ctx context.Context, obj *api.LoadScenario) (httpapi.LoadScenarioView, error) {
	names, err := s.inUse(ctx, obj.Name)
	return httpapi.LoadScenarioView{
		Name: obj.Name, UID: string(obj.UID), Spec: obj.Spec, InUse: names, CreatedAt: obj.CreationTimestamp,
	}, err
}
func (s *evaluationLoadService) list(ctx context.Context) (httpapi.LoadScenarioList, error) {
	result := httpapi.LoadScenarioList{Items: []httpapi.LoadScenarioView{}}
	var objects api.LoadScenarioList
	if err := s.client.List(ctx, &objects, client.InNamespace(s.namespace)); err != nil {
		return result, err
	}
	for i := range objects.Items {
		v, err := s.view(ctx, &objects.Items[i])
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, v)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Name < result.Items[j].Name })
	return result, nil
}
func (s *evaluationLoadService) get(ctx context.Context, name string) (httpapi.LoadScenarioView, error) {
	var obj api.LoadScenario
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &obj); err != nil {
		return httpapi.LoadScenarioView{}, err
	}
	return s.view(ctx, &obj)
}
func (s *evaluationLoadService) preflight(
	ctx context.Context, request httpapi.LoadScenarioRequest,
) (httpapi.LoadScenarioPreflight, error) {
	result := httpapi.LoadScenarioPreflight{
		Checks: []httpapi.EvaluationCheck{}, LiveWorkloads: map[string]httpapi.LiveWorkload{},
	}
	add := func(id string, err error) {
		check := httpapi.EvaluationCheck{ID: id, Passed: err == nil}
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
			result.LiveWorkloads[ref.Name] = httpapi.LiveWorkload{UID: string(obj.UID), Generation: obj.Generation}
			if obj.UID != ref.UID || obj.Generation != ref.Generation {
				err = fmt.Errorf("workload identity or generation differs")
			}
		}
		add("workload:"+ref.Name, err)
	}
	refs := []api.EvaluationEvidenceReference{request.Spec.SeedRef, request.Spec.PayloadManifestRef}
	for _, ref := range request.Spec.TraceRefs {
		refs = append(refs, ref)
	}
	for _, ref := range []*api.EvaluationEvidenceReference{request.Spec.RateBasisRef, request.Spec.KappaRef} {
		if ref != nil {
			refs = append(refs, *ref)
		}
	}
	for _, ref := range refs {
		object, err := s.evidence.Stat(ctx, evidencestore.ObjectLocation{Store: ref.Store, Key: ref.Key})
		if err == nil && object.Size != ref.Size {
			err = fmt.Errorf("evidence size differs: live %d, requested %d", object.Size, ref.Size)
		}
		add("evidence:"+ref.Key, err)
	}
	return result, nil
}
func (s *evaluationLoadService) checked(ctx context.Context, request httpapi.LoadScenarioRequest) error {
	result, err := s.preflight(ctx, request)
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
func (s *evaluationLoadService) createObject(
	ctx context.Context, request httpapi.LoadScenarioRequest, actor string,
) (httpapi.EvaluationIdentity, error) {
	obj := api.LoadScenario{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: s.namespace, Name: request.Name,
			Annotations: map[string]string{"edge.dacs.io/requested-by": actor},
		},
		Spec: request.Spec,
	}
	err := s.client.Create(ctx, &obj)
	return httpapi.EvaluationIdentity{Name: obj.Name, UID: string(obj.UID)}, err
}
func (s *evaluationLoadService) create(
	ctx context.Context, request httpapi.LoadScenarioRequest, actor string,
) (httpapi.EvaluationIdentity, error) {
	if err := s.checked(ctx, request); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	return s.createObject(ctx, request, actor)
}
func (s *evaluationLoadService) removable(ctx context.Context, name, uid string) (*api.LoadScenario, error) {
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
func (s *evaluationLoadService) delete(ctx context.Context, name, uid string) (httpapi.EvaluationIdentity, error) {
	identity := httpapi.EvaluationIdentity{Name: name, UID: uid}
	obj, err := s.removable(ctx, name, uid)
	if err != nil {
		return identity, err
	}
	pinned := types.UID(uid)
	return identity, s.client.Delete(ctx, obj, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pinned}})
}
func (s *evaluationLoadService) replace(
	ctx context.Context, name, uid string, request httpapi.LoadScenarioRequest, actor string,
) (httpapi.EvaluationIdentity, error) {
	if request.Name != "" && request.Name != name {
		return httpapi.EvaluationIdentity{}, loadError("InvalidInput", "body name must match path name")
	}
	request.Name = name
	if err := s.checked(ctx, request); err != nil {
		return httpapi.EvaluationIdentity{}, err
	}
	if _, err := s.delete(ctx, name, uid); err != nil {
		return httpapi.EvaluationIdentity{}, err
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
		return httpapi.EvaluationIdentity{}, fmt.Errorf("이전 객체 삭제됨; replacement wait: %w", err)
	}
	identity, err := s.createObject(ctx, request, actor)
	if err != nil {
		return identity, fmt.Errorf("이전 객체 삭제됨; recreate: %w", err)
	}
	return identity, nil
}
func evaluationInputKind(key string) string {
	switch path.Base(key) {
	case "bundle.json":
		return "payload"
	case "seeds.json":
		return "seed"
	case "rate-basis.json":
		return "rateBasis"
	case "kappa.json":
		return "kappa"
	}
	return "other"
}
func (s *evaluationLoadService) inputs(ctx context.Context) (httpapi.EvaluationInputList, error) {
	result := httpapi.EvaluationInputList{Items: []httpapi.EvaluationInput{}}
	objects, err := s.lister.List(ctx, "experiments/_inputs/")
	if err != nil {
		return result, err
	}
	for _, obj := range objects {
		relative := strings.TrimPrefix(obj.Key, "experiments/_inputs/")
		if relative == obj.Key || !strings.Contains(relative, "/") ||
			strings.HasPrefix(relative, "/") || strings.HasSuffix(relative, "/") {
			continue
		}
		set, _, _ := strings.Cut(relative, "/")
		result.Items = append(result.Items, httpapi.EvaluationInput{
			Store: obj.Store, Key: obj.Key, Size: obj.Size, Kind: evaluationInputKind(obj.Key),
			Set: set, Name: set + " / " + path.Base(obj.Key),
		})
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Key < result.Items[j].Key })
	return result, nil
}
