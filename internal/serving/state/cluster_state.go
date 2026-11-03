package state

import (
	context "context"
	serving "github.com/lab-paper-code/chill/internal/serving"
	kubernetes "k8s.io/client-go/kubernetes"
	sync "sync"
	time "time"
)

// ClusterState is the one shared reservation and removal authority. The
// composition root constructs it before the owners that observe or act on it.
type ClusterState struct {
	reservation ReservationState
	observation ObserverState
	store       objectStore
	kube        kubernetes.Interface
	namespace   string
}

func NewClusterState(store objectStore, kube kubernetes.Interface, namespace string) *ClusterState {
	return &ClusterState{store: store, kube: kube, namespace: namespace,
		reservation: ReservationState{runs: map[string]*ActionRun{}},
		observation: ObserverState{removingInstances: map[string]bool{}}}
}

func (s *ClusterState) Reservations() *ReservationState { return &s.reservation }

// ReservationState owns the existing scheduler locks and the single run ledger.
// Calls that need both locks acquire tickMu before mu.
type ReservationState struct {
	mu                    sync.Mutex
	tickMu                sync.Mutex
	runs                  map[string]*ActionRun
	state                 SchedulerControllerState
	replacements          []serving.SchedulerReplacement
	replacementObservedAt time.Time
	// unrouted holds when each captured instance last lost every route weight.
	// overloaded holds the slots prepared while keep was in Overload: zero
	// until the instance is Ready, then the end of its hold.
	unrouted, overloaded map[string]time.Time
}

// LockState and UnlockState serialize the ledger and controller configuration
// with the existing shared mutex; they do not return or replace that mutex.
func (s *ReservationState) LockState()   { s.mu.Lock() }
func (s *ReservationState) UnlockState() { s.mu.Unlock() }

// LockTick and UnlockTick retain the existing outer control serialization.
func (s *ReservationState) LockTick()   { s.tickMu.Lock() }
func (s *ReservationState) UnlockTick() { s.tickMu.Unlock() }

// ObserverState keeps the existing native-source mutex distinct from the
// reservation locks. Removal markers share this mutex with source publication.
type ObserverState struct {
	mu                sync.Mutex
	removingInstances map[string]bool
}

type ActionRun struct {
	action               serving.Action
	result               *OperationResult
	cancel               context.CancelFunc
	started              time.Time
	scope                ResourceScope
	owner                string
	reserved             bool
	returnObserved       bool
	prepareIdentity      string
	inheritedReservation bool
	routeReceipts        map[string]*ActionRun
	mayTouch             map[string]bool
	proofParent          *ActionRun
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
		if !operation.ControlsWorkload(uid) {
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
