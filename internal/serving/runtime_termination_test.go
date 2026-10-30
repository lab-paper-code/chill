package serving

import "testing"

// SCH-26/50, AC-45: admission-stop never proves physical termination; exact
// terminal facts survive both outstanding-call merges and delayed Ready probes.
func TestRuntimeTerminationIsPhysicalAndMonotonicAC45(t *testing.T) {
	for _, outstanding := range []int{0, 1} {
		r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
		source := r.instances[wanted.ID].ReadyInstance
		source.State.PodUID, source.State.TargetID = "old-pod", "shared-slot"
		if err := r.InstallInstances([]ReadyInstance{source}); err != nil {
			t.Fatal(err)
		}
		declared := wanted
		declared.Terminated = true
		if err := r.ValidateSchedulerWorkloadsTarget(SchedulerCandidate{Instances: []SchedulerInstance{declared},
			Routing: map[string]map[string]int32{"w1": {wanted.ID: 1}, "w2": {}}}, []string{"w1"}); err == nil {
			t.Fatal("terminal route declaration passed pre-ACK validation")
		}
		if stopped, err := r.StopSchedulerInstance(wanted.ID); err != nil || !stopped {
			t.Fatalf("logical stop: %v %v", stopped, err)
		}
		if r.instances[wanted.ID].State.Terminated {
			t.Fatal("admission-stop manufactured termination proof")
		}
		if err := r.RetirePod(source.State.PodUID); err == nil {
			t.Fatal("logical stop was accepted as physical retirement proof")
		}
		invalid := source
		invalid.State.Terminated = true
		if err := r.InstallInstances([]ReadyInstance{invalid}); err == nil {
			t.Fatal("contradictory Ready terminal input was accepted")
		}
		r.instances[wanted.ID].outstanding = outstanding
		terminal := source
		terminal.State.Ready, terminal.State.Stopped, terminal.State.Terminated = false, true, true
		if err := r.InstallInstances([]ReadyInstance{terminal}); err != nil {
			t.Fatal(err)
		}
		if !r.instances[wanted.ID].State.Terminated || r.instances[wanted.ID].State.Ready || r.instances[wanted.ID].outstanding != outstanding {
			t.Fatal("terminal merge lost proof or released outstanding ownership")
		}
		if err := r.InstallInstances([]ReadyInstance{source}); err != nil {
			t.Fatal(err)
		}
		if !r.instances[wanted.ID].State.Terminated || r.instances[wanted.ID].State.Ready || !r.instances[wanted.ID].State.Stopped {
			t.Fatal("late successful probe resurrected a terminated executor")
		}
		target := SchedulerCandidate{Instances: []SchedulerInstance{wanted}, Routing: map[string]map[string]int32{"w1": {wanted.ID: 1}, "w2": {}}}
		if err := r.ApplySchedulerTarget(target); err == nil {
			t.Fatal("positive routing accepted a terminal destination")
		}
		replacement := source
		replacement.State.ID, replacement.State.PodUID = "replacement", "replacement-pod"
		if err := r.InstallInstances([]ReadyInstance{source, replacement}); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		resolved, err := r.schedulerSlotLocked(source.State.TargetID)
		r.mu.Unlock()
		if err != nil || resolved == nil || resolved.State.ID != replacement.State.ID || !resolved.State.Ready {
			t.Fatalf("dead alias blocked a new physical identity: %+v %v", resolved, err)
		}
		if err := r.ConfirmInstanceStopped(replacement.State.ID); err != nil || !r.instances[replacement.State.ID].State.Terminated {
			t.Fatalf("exact termination was not retained: %v", err)
		}
	}
}
