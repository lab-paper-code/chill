package operations

import (
	"context"
	"reflect"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These consumer contracts keep physical execution independent of cluster
// reconciliation while preserving the existing concrete owners and locks.
type objectStore interface {
	Client() client.Client
}

type removalState interface {
	MarkRemoving(string)
	ClearRoutedRemovals(servingstate.Operation, map[string]map[string]int32)
}

type sourceObserver interface {
	Refresh(context.Context) error
	RefreshWithin(context.Context, time.Duration) error
	RestoreRemovalSource(*core.Pod, string, string) error
	ObserveRemovalStopped(*core.Pod) bool
	RetireRemovedSource(types.UID, *serving.Runtime) error
	BeginNodeMove(context.Context, string) error
	ClearNodeMove(string)
	RecordPowerReboot(serving.NodeReference, serving.PowerReboot)
	PublishPreparedSource(*core.Pod, serving.NodeReference) error
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
