package serving

import (
	"fmt"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// The executor's choice, the gateway's prediction and the replay take the same
// late request on an idle executor, and reject it when a request with a later
// deadline is queued ahead of it.
func TestIdleExecutorTakesLateRequestInExecutorGatewayAndReplay(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprint("busy=", busy), func(t *testing.T) {
			settings, snap := schedulerFixture()
			settings.AutonomousWorkloads = nil
			snap.Arrivals = nil
			// The fixture's 10 ms batch cannot meet a 1 ms SLO.
			d := snap.Workloads[0]
			d.SLO.TargetLatency = time.Millisecond
			ahead := *d
			ahead.UID, ahead.SLO.TargetLatency = "ahead", time.Second
			state := func(id string, d *Definition) firstfloor.RequestState {
				return firstfloor.RequestState{Context: firstfloor.RequestContext{RequestID: id, WorkloadUID: d.UID, Definition: d.Identity(), AdmissionPolicy: string(d.Admission.Policy), SLO: firstfloor.SLO{Percentile: d.SLO.Percentile, TargetMS: float64(d.SLO.TargetLatency) / 1e6}}, Phase: phaseQueued}
			}
			candidate := state("candidate", d)
			snap.Outstanding = []SchedulerRequest{{ID: "candidate", Definition: d, ReceivedAt: snap.At, Phase: phasePending}}
			var observed []firstfloor.RequestState
			if busy {
				observed = append(observed, state("ahead", &ahead))
				zero := 0.0
				snap.Outstanding = append(snap.Outstanding, SchedulerRequest{ID: "ahead", Definition: &ahead, ReceivedAt: snap.At, Phase: phaseQueued, Instance: "i1", NativeSenderElapsedMS: &zero})
			}
			policy := schedulerPolicy(snap.Instances[0])

			choice := firstfloor.ChooseBatch(policy, append(observed, candidate), new(int64))
			executorTakes := choice.Size == 1 && choice.WorkloadUID == d.UID
			executorRejects := choice.Reason == firstfloor.OutcomeDeadlineDrop && choice.RequestID == "candidate"

			estimate := EstimateAdmission(AdmissionInput{Policy: policy, Observed: observed, LastServiceEndAgeNanos: new(int64), Candidate: candidate})
			gatewayTakes := estimate.Known && admissionCandidateAvailable(estimate, false, d.SLO.TargetLatency)

			e := schedulerRequireEstimate(t, schedulerKeepForTest(t, settings, snap), d.UID)
			replayCompletes := e.Samples == 1 && e.PredictedOutcomes["late"] == 1 && e.PredictedOutcomes["rejected"] == 0
			replayRejects := e.Samples == 0 && e.PredictedOutcomes["rejected"] == 1

			if !busy && (!executorTakes || !gatewayTakes || !replayCompletes) {
				t.Fatalf("idle executor must take the late request: choice=%+v estimate=%+v replay=%+v", choice, estimate, e)
			}
			if busy && (!executorRejects || gatewayTakes || !replayRejects) {
				t.Fatalf("queued work must still be protected by rejection: choice=%+v estimate=%+v replay=%+v", choice, estimate, e)
			}
		})
	}
}
