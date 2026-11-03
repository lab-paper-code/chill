package state

import (
	"context"
	"reflect"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// objectStore is the object access consumed by cluster observation and deletion
// reservation. The composition root supplies the same concrete Kubernetes Store.
type objectStore interface {
	Client() client.Client
	SourceResources(context.Context, []core.Pod, string) (map[types.UID]client.Object, error)
	OwnedResources(context.Context, types.UID, string) ([]client.Object, error)
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
