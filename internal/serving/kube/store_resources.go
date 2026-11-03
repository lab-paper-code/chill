package kube

import (
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *Store) SourceResources(ctx context.Context, pods []corev1.Pod, namespace string) (map[types.UID]client.Object, error) {
	all := make(map[types.UID]client.Object, len(pods))
	for i := range pods {
		all[pods[i].UID] = &pods[i]
	}
	for _, list := range []client.ObjectList{&appsv1.DeploymentList{}, &appsv1.ReplicaSetList{}} {
		if err := s.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
			return nil, err
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			object := item.(client.Object)
			all[object.GetUID()] = object
		}
	}
	return all, nil
}

func (s *Store) OwnedResources(ctx context.Context, uid types.UID, namespace string) ([]client.Object, error) {
	var pods corev1.PodList
	if err := s.client.List(ctx, &pods, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	all, err := s.SourceResources(ctx, pods.Items, namespace)
	if err != nil {
		return nil, err
	}
	result := []client.Object{}
	for _, object := range all {
		owner, err := servingstate.ResourceWorkloadOwner(object, all)
		if err != nil && owner == string(uid) {
			return nil, err
		}
		if err == nil && owner == string(uid) {
			result = append(result, object)
		}
	}
	return result, nil
}
