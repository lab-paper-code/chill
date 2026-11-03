package kube

import (
	"context"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

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
