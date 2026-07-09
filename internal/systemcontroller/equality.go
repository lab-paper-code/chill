package systemcontroller

import (
	"reflect"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func statusesEqual(left, right edgev1alpha1.ChillSystemStatus) bool {
	return reflect.DeepEqual(left, right)
}
