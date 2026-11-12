package kube

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

const (
	approvalReadyField      = "Ready"
	approvalAllocationField = "Allocation"
	approvalRoutableField   = "Routable"
)

func TestSchedulerApprovalInstanceFields(t *testing.T) {
	volatile := map[string]bool{"ObservedAt": true, "NativeSpanAt": true, "LastServiceEndAgeUpperNanos": true, "LastServiceEndAgeNanos": true, "ArrivalAgesMS": true, "AdmissionIdleUnknown": true}
	compared := map[string]bool{}
	for _, name := range []string{"ID", "PodUID", "PodName", "TargetID", "OwnerWorkloadUID", "Node", "Profile", "ProfileID", "RuntimeImage", "RunnerCommand", "ActualImageID", "DesiredRuntimeImage", "ReplacementReason", "WorkloadUID", "Allocation", "Ready", "Stopped", "Terminated", "StartupFailure", "Routable", "Reasons", "Capacity", "Weight", "MaxBatch", "FixedBatchSize", "MaxWait", "IdleMeter", "IdleScope", "UnroutedSince", "HeldUntil"} {
		compared[name] = true
	}
	typ := reflect.TypeFor[serving.SchedulerInstance]()
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if volatile[name] == compared[name] {
			t.Errorf("SchedulerInstance.%s must have exactly one approval classification", name)
		}
	}
	for name := range volatile {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("unknown volatile field %s", name)
		}
	}
	for name := range compared {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("unknown compared field %s", name)
		}
	}
}

func TestSchedulerApprovalIgnoresTrafficInstanceFields(t *testing.T) {
	for _, option := range []bool{false, true} {
		for _, field := range []string{"NativeSpanAt", "LastServiceEndAgeUpperNanos", "AdmissionIdleUnknown", "ObservedAt", "LastServiceEndAgeNanos", "ArrivalAgesMS", approvalReadyField, approvalAllocationField, approvalRoutableField} {
			t.Run(field+map[bool]string{false: "/instance", true: "/option"}[option], func(t *testing.T) {
				before := serving.SchedulerInstance{}
				after := before
				age := int64(100)
				switch field {
				case "NativeSpanAt":
					after.NativeSpanAt = time.Now()
				case "LastServiceEndAgeUpperNanos":
					after.LastServiceEndAgeUpperNanos = &age
				case "AdmissionIdleUnknown":
					after.AdmissionIdleUnknown = true
				case "ObservedAt":
					after.ObservedAt = time.Now()
				case "LastServiceEndAgeNanos":
					after.LastServiceEndAgeNanos = &age
				case "ArrivalAgesMS":
					after.ArrivalAgesMS = map[string][]float64{"workload": {1}}
				case approvalReadyField:
					after.Ready = true
				case approvalAllocationField:
					after.Allocation.MemoryBytes = 1
				case approvalRoutableField:
					after.Routable = map[string]bool{"workload": true}
				}
				a, b := serving.SchedulerSnapshot{}, serving.SchedulerSnapshot{}
				if option {
					a.Options = []serving.SchedulerOption{{Instance: before}}
					b.Options = []serving.SchedulerOption{{Instance: after}}
				} else {
					a.Instances = []serving.SchedulerInstance{before}
					b.Instances = []serving.SchedulerInstance{after}
				}
				equal := reflect.DeepEqual(schedulerApprovalInputs(serving.SchedulerSettings{}, a), schedulerApprovalInputs(serving.SchedulerSettings{}, b))
				realChange := field == approvalReadyField || field == approvalAllocationField || field == approvalRoutableField
				if equal == realChange {
					t.Fatalf("approval equality=%t for %s; real change=%t", equal, field, realChange)
				}
			})
		}
	}
}

func TestSchedulerApprovalTrafficCaptureLaunchesTarget(t *testing.T) {
	for _, change := range []string{"traffic", approvalReadyField, approvalAllocationField, approvalRoutableField} {
		t.Run(change, func(t *testing.T) {
			c, _ := tickReadySubsetController(t, nil)
			c.config.Settings.AutonomousWorkloads = nil
			c.config.Settings.Controls.Routing = true
			c.runtime.EnableSchedulerRouting()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			snapshot, err := c.captureReconciled(ctx, time.Now(), c.config, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			uid := snapshot.Workloads[0].UID
			ready := snapshot.Instances[0]
			ready.WorkloadUID = uid
			seed := serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{ready},
				Routing: map[string]map[string]int32{uid: {ready.ID: 1000}}}
			if err := c.runtime.ApplySchedulerWorkloadsTarget(seed, []string{uid}); err != nil {
				t.Fatal(err)
			}
			snapshot, err = c.captureReconciled(ctx, time.Now(), c.config, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			target := serving.SchedulerCandidate{ID: "traffic-clock-target", Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: snapshot.Routing,
				Actions: []serving.Action{{ID: "traffic-clock-route", Kind: servingstate.OperationRoute}}}
			// Explicitly retain the unready destination at zero weight. Positive
			// routing remains the admitted Keep plan; launch must acknowledge the map.
			target.Routing = map[string]map[string]int32{uid: {
				snapshot.Instances[0].ID: snapshot.Routing[uid][snapshot.Instances[0].ID],
				snapshot.Instances[1].ID: 0,
			}}
			// The first capture predates completion reconciliation. The second capture
			// has unchanged physical authority but fresh traffic-derived spans.
			stale := snapshot
			stale.Instances = append([]serving.SchedulerInstance(nil), snapshot.Instances...)
			lower, upper := int64(0), int64(time.Millisecond)
			stale.Instances[0].NativeSpanAt = stale.Instances[0].ObservedAt
			stale.Instances[0].LastServiceEndAgeNanos = &lower
			stale.Instances[0].LastServiceEndAgeUpperNanos = &upper
			stale.Instances[0].AdmissionIdleUnknown = true
			switch change {
			case approvalReadyField:
				stale.Instances[0].Ready = !stale.Instances[0].Ready
			case approvalAllocationField:
				stale.Instances[0].Allocation.MemoryBytes++
			case approvalRoutableField:
				stale.Instances[0].Routable = map[string]bool{uid: false}
			}
			c.state.Snapshot = snapshot
			c.tickMu.Lock()
			approved, config, err := c.recheckEvaluationTarget(ctx, c.config, stale, target, &observation.SchedulerControlObservation{}, func(string, *time.Duration) {})
			c.tickMu.Unlock()
			if change != "traffic" {
				if err == nil || !strings.Contains(err.Error(), "evaluation invalidated") {
					t.Fatalf("changed %s was not discarded: %v", change, err)
				}
				if len(c.runs) != 0 {
					t.Fatal("discarded target launched an action")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			c.publishSnapshotLocked(&approved)
			c.mu.Unlock()
			c.launch(ctx, target, config)
			c.wg.Wait()
			c.mu.Lock()
			defer c.mu.Unlock()
			run := c.runs["traffic-clock-route"]
			if run == nil || run.action.Status != servingstate.NativeOperationComplete {
				t.Fatalf("approved target did not launch and acknowledge its route: %+v", run)
			}
		})
	}
}
