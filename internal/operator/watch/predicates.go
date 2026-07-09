package watch

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// NamedObject accepts events for one Kubernetes object name, optionally scoped
// to a namespace.
func NamedObject(namespace, name string) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return isNamedObject(e.Object, namespace, name) },
		DeleteFunc: func(e event.DeleteEvent) bool { return isNamedObject(e.Object, namespace, name) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return isNamedObject(e.ObjectNew, namespace, name) || isNamedObject(e.ObjectOld, namespace, name)
		},
		GenericFunc: func(e event.GenericEvent) bool { return isNamedObject(e.Object, namespace, name) },
	}
}

// CreateDelete accepts create/delete events and ignores updates.
func CreateDelete() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func isNamedObject(obj client.Object, namespace, name string) bool {
	if obj == nil {
		return false
	}
	if namespace != "" && obj.GetNamespace() != namespace {
		return false
	}
	return obj.GetName() == name
}
