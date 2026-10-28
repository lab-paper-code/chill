package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestArrivalCountsMergeReplacementAndCoverage(t *testing.T) {
	control, _ := NewControl(2)
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	from := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Second)
	to := from.Add(time.Second)
	plan := testPlan("arrivals")
	plan.Participants = 2
	plan.Definitions = []*serving.Definition{{UID: "workload", Name: "workload", Generation: 1}}
	if err := control.Publish(plan); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channels := []*channel{newChannel(ctx, nil), newChannel(ctx, nil)}
	participants := make([]*member, 2)
	for index, id := range []string{"router-a", "router-b"} {
		participant, err := control.join(id, channels[index])
		if err != nil {
			t.Fatal(err)
		}
		participants[index] = participant
		participant.JoinedAt = from.Add(-time.Second)
		report := serving.GatewayReport{RouterID: id, PlanVersion: plan.PlanVersion, AppliedAt: from.Add(-time.Second), ObservedAt: time.Now().UTC(), Arrivals: []serving.GatewayArrivals{{From: from, To: to, WorkloadUID: "workload", Received: uint64(index + 2), Validated: 1}}}
		if err = control.accept(participant, channels[index], message{Type: "applied", Generation: participant.Generation, Report: &report}); err != nil {
			t.Fatal(err)
		}
	}
	totals, err := control.ArrivalCounts(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 1 || totals[0].Received != 5 || totals[0].Validated != 2 || !totals[0].From.Equal(from) || !totals[0].To.Equal(to) {
		t.Fatalf("incorrect global counts: %+v", totals)
	}
	replacement := participants[0].Report
	replacement.Arrivals = []serving.GatewayArrivals{{From: from, To: to, WorkloadUID: "workload", Received: 4, Validated: 2}}
	for range 2 {
		if err = control.accept(participants[0], channels[0], message{Type: "report", Generation: participants[0].Generation, Report: &replacement}); err != nil {
			t.Fatal(err)
		}
	}
	totals, err = control.ArrivalCounts(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if totals[0].Received != 7 || totals[0].Validated != 3 {
		t.Fatalf("report retransmission double-counted population: %+v", totals)
	}
	participants[1].Report.Arrivals = nil
	if _, err = control.ArrivalCounts(from, to); !isIncomplete(err) {
		t.Fatalf("missing stream became zero: %v", err)
	}
	if _, err = control.ArrivalCounts(from.Add(time.Millisecond), to); !isIncomplete(err) {
		t.Fatalf("partial second accepted: %v", err)
	}
}
func isIncomplete(err error) bool {
	var failure *serving.Error
	return errors.As(err, &failure) && failure.Code == "IncompleteObservation"
}

func TestArrivalCountsExplicitZeroAndStaleReport(t *testing.T) {
	control, _ := NewControl(1)
	t.Cleanup(func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	})
	from := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Second)
	to := from.Add(time.Second)
	source := &member{RouterState: RouterState{RouterID: "router", Connected: true, JoinedAt: from.Add(-time.Second), LastSeen: time.Now(), ArrivalWindow: time.Minute, Definitions: map[string][]DefinitionPeriod{"workload": {{JoinedAt: from.Add(-time.Second)}}}, Report: serving.GatewayReport{RouterID: "router", ObservedAt: time.Now(), Arrivals: []serving.GatewayArrivals{{From: from, To: to, WorkloadUID: "workload"}}}}}
	control.members["router"] = source
	totals, err := control.ArrivalCounts(from, to)
	if err != nil || len(totals) != 1 || totals[0].Received != 0 {
		t.Fatalf("explicit observed zero lost: %+v %v", totals, err)
	}
	source.LastSeen = time.Now().Add(-FreshnessTimeout - time.Second)
	if _, err = control.ArrivalCounts(from, to); !isIncomplete(err) {
		t.Fatalf("stale report accepted: %v", err)
	}
}
