package kube

import (
	"context"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/client-go/kubernetes"
)

// ClusterState is the one shared reservation and removal authority. The
// composition root constructs it before the owners that observe or act on it.
type ClusterState struct {
	reservation reservationState
	observation observerState
	store       *Store
	kube        kubernetes.Interface
	namespace   string
}

func NewClusterState(store *Store, kube kubernetes.Interface, namespace string) *ClusterState {
	return &ClusterState{store: store, kube: kube, namespace: namespace,
		reservation: reservationState{runs: map[string]*actionRun{}},
		observation: observerState{removingInstances: map[string]bool{}}}
}

// reservationState owns the existing scheduler locks and the single run ledger.
// Calls that need both locks acquire tickMu before mu.
type reservationState struct {
	mu     sync.Mutex
	tickMu sync.Mutex
	runs   map[string]*actionRun
	state  SchedulerControllerState
}

// observerState keeps the existing native-source mutex distinct from the
// reservation locks. Removal markers share this mutex with source publication.
type observerState struct {
	mu                sync.Mutex
	removingInstances map[string]bool
}

type actionRun struct {
	action               serving.Action
	result               *OperationResult
	cancel               context.CancelFunc
	started              time.Time
	scope                resourceScope
	owner                string
	reserved             bool
	returnObserved       bool
	prepareIdentity      string
	inheritedReservation bool
	routeReceipts        map[string]*actionRun
	mayTouch             map[string]bool
	proofParent          *actionRun
	replacements         []serving.SchedulerReplacement
	admittedControls     serving.SchedulerControls
}

func (s *ClusterState) MarkRemoving(id string) {
	s.observation.mu.Lock()
	s.observation.removingInstances[id] = true
	s.observation.mu.Unlock()
}

func (s *ClusterState) ClearRoutedRemovals(operation Operation, weights map[string]map[string]int32) {
	s.observation.mu.Lock()
	for uid, route := range weights {
		if !operation.controlsWorkload(uid) {
			continue
		}
		for id, weight := range route {
			if weight > 0 {
				delete(s.observation.removingInstances, id)
			}
		}
	}
	s.observation.mu.Unlock()
}
