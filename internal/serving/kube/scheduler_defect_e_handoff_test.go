package kube

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// T10 keeps a separate unavailable scope throughout actual handoff/cleanup:
// positive executed source weights still veto stop, and a Failed owner may
// complete only the cleanup tail of an already completed stop.
func TestDefectEHandoffFencesWithResidualUnknownScope(t *testing.T) {
	for _, positive := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed-stop-cleanup", true: "positive-executed-source"}[positive], func(t *testing.T) {
			c, goal := admittedHandoff(t, positive)
			if positive {
				handoffDispatch(t, c, goal, goal.Actions...)
			} else {
				handoffDispatch(t, c, goal, goal.Actions[2])
				requireCompletedContinuationAction(t, c.runs["stop-source"], "real source stop")
				c.config.OnAction = func(result servingstate.OperationResult) error {
					if result.Operation.ID == "failed-route" && result.Status == starting {
						return errors.New("persist route start refused")
					}
					return nil
				}
				handoffDispatch(t, c, goal, goal.Actions[6])
				c.config.OnAction = nil
			}
			lost := loseDefectEProbe(c, "10.96.0.12:8080")
			lost.Store(true)
			for range 4 {
				if err := c.Continue(c.ctx, time.Now()); err != nil {
					t.Fatal(err)
				}
				c.wg.Wait()
			}
			parent := c.runs[transitionRunKind+"/"+goal.ID]
			unknown := goal.Actions[5].Node
			if parent.action.Status != servingstate.NativeOperationFailed || !parent.reserved || !slices.Contains(parent.scope.Nodes, unknown) || c.runs["unstarted-stop"] != nil {
				t.Fatalf("failed handoff lost unknown responsibility or restarted original stop: %+v", parent.action)
			}
			if positive {
				if c.runs["stop-source"] != nil {
					t.Fatal("actual positive rebased source was stopped")
				}
			} else {
				for _, id := range []string{"drain-source", "remove-source"} {
					requireCompletedContinuationAction(t, c.runs[id], "already-complete stop cleanup must progress")
				}
			}
			if !c.reservedNodes()[unknown.UID] || slices.ContainsFunc(c.State().Actions, func(action serving.Action) bool {
				return action.ID == "unstarted-stop" && action.Status == servingstate.NativeOperationRunning
			}) {
				t.Fatal("residual unknown scope escaped handoff fence")
			}
		})
	}
}
