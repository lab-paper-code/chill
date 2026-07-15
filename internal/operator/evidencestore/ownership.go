package evidencestore

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func requireControlledBySystem(object client.Object, system *edgev1alpha1.ChillSystem) error {
	if metav1.IsControlledBy(object, system) {
		return nil
	}
	if owner := metav1.GetControllerOf(object); owner != nil {
		return &controllerutil.AlreadyOwnedError{Object: object, Owner: *owner}
	}
	return fmt.Errorf("%T %s/%s exists without a controller reference", object, object.GetNamespace(), object.GetName())
}
