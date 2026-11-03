package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPreEffectProofDoesNotChangeOperationJSON(t *testing.T) {
	result := OperationResult{Operation: Operation{ID: "prepare-proof", Kind: OperationPrepare}}
	before, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	result.EffectsNotStarted = true
	after, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("internal pre-effect proof changed evidence JSON: %s != %s", before, after)
	}
}

func TestCompletedPreparationRetainsAbnormalExitNotNormalRetirement(t *testing.T) {
	node := serving.NodeReference{Name: "node", UID: "node-uid"}
	wanted := serving.SchedulerInstance{Node: node, Profile: &serving.Profile{ID: "profile"}}
	result := OperationResult{Operation: Operation{Kind: OperationPrepare, Prepare: &PrepareSpec{Instance: wanted, MemoryBytes: 1024}},
		Status:      NativeOperationComplete,
		Preparation: &PreparationEvidence{PodUID: "pod-uid"}}
	snapshot := serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{{PodUID: "pod-uid", Terminated: true}}}
	if fact := result.PreparationFailure(snapshot); fact != nil {
		t.Fatal("normal retirement turned successful preparation into a placement failure")
	}
	snapshot.Instances[0].StartupFailure = &serving.PreparationFailureObservation{Classification: serving.PreparationDeterministic,
		ReasonKey: "process-exit:firstfloor:OOMKilled:137", Reason: "firstfloor: OOMKilled (exit 137)"}
	fact := result.PreparationFailure(snapshot)
	if fact == nil || fact.PodUID != "pod-uid" || fact.Allocation.MemoryBytes != 1024 || fact.Reason != snapshot.Instances[0].StartupFailure.Reason {
		t.Fatalf("abnormal exit after Ready lost its physical constraint: %+v", fact)
	}
	snapshot.Instances[0].StartupFailure = &serving.PreparationFailureObservation{Classification: serving.PreparationTransient,
		ReasonKey: "external-reclamation:deleting", Reason: "termination after deletion requested"}
	if fact := result.PreparationFailure(snapshot); fact != nil {
		t.Fatal("an intentional removal after successful prepare became a startup constraint")
	}
	result.Status, result.Error = NativeOperationPartial, "context canceled"
	snapshot.Instances = nil
	if fact := result.PreparationFailure(snapshot); fact != nil {
		t.Fatal("operator cancellation became a persistent placement constraint")
	}
}

func TestPreparationFactsDistinguishProcessArtifactAndEnvironmentFailures(t *testing.T) {
	for _, test := range []struct{ name, classification string }{
		{"OOM", serving.PreparationDeterministic},
		{"started-exit", serving.PreparationDeterministic},
		{"before-process-network", serving.PreparationTransient},
		{"before-process-permission", serving.PreparationDeterministic},
		{"pull-network", serving.PreparationTransient},
		{"missing-image-event", serving.PreparationDeterministic},
		{"intentional-delete", serving.PreparationTransient},
		{"permission", serving.PreparationDeterministic},
		{"eviction", serving.PreparationTransient},
		{"preemption", serving.PreparationTransient},
		{"not-ready", serving.PreparationTransient},
		{"opaque", serving.PreparationUnknownTransient},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := &core.Pod{Status: core.PodStatus{ContainerStatuses: []core.ContainerStatus{{Name: FirstfloorContainerName,
				State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "ErrImagePull", Message: "network connection timed out"}}}}}}
			node := &core.Node{Status: core.NodeStatus{Conditions: []core.NodeCondition{{Type: core.NodeReady, Status: core.ConditionTrue}}}}
			var events []core.Event
			switch test.name {
			case "OOM":
				pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}
			case "started-exit":
				pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: "Error", ExitCode: 1, StartedAt: meta.Now()}}
			case "before-process-network":
				pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: "StartError", ExitCode: 128, Message: "network setup timed out"}}
			case "before-process-permission":
				pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: "StartError", ExitCode: 128, Message: "exec: permission denied"}}
			case "intentional-delete":
				now := meta.Now()
				pod.DeletionTimestamp = &now
				pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: "Error", ExitCode: 143, StartedAt: now}}
			case "missing-image-event":
				events = []core.Event{{Reason: "Failed", Message: "rpc error: code = NotFound desc = manifest unavailable"}}
			case "permission":
				pod.Status.ContainerStatuses[0].State.Waiting.Message = "rpc error: code = PermissionDenied"
			case "eviction":
				pod.Status.Phase, pod.Status.Reason = core.PodFailed, podEvictedReason
			case "preemption":
				events = []core.Event{{Reason: "Preempted"}}
			case "not-ready":
				pod.Status.ContainerStatuses = nil
				node.Status.Conditions[0].Status = core.ConditionFalse
			case "opaque":
				pod.Status.ContainerStatuses = nil
			}
			observed, failed := ClassifyPreparationFailure(pod, node, events, errors.New("unrecognized startup failure"), true)
			if !failed || observed.Classification != test.classification || observed.ReasonKey == "" || observed.Reason == "" {
				t.Fatalf("failure was misclassified or lost its observed reason: %+v", observed)
			}
			wanted := serving.SchedulerInstance{Profile: &serving.Profile{ID: "profile"}}
			result := OperationResult{Operation: Operation{Kind: OperationPrepare, Prepare: &PrepareSpec{Instance: wanted, MemoryBytes: 1024}},
				Status:      NativeOperationPartial,
				Preparation: &PreparationEvidence{PodUID: "observed-pod", Failure: &observed}}
			fact := result.PreparationFailure(serving.SchedulerSnapshot{})
			if fact == nil || fact.Classification != test.classification {
				t.Fatalf("operation fact changed startup classification: %+v", fact)
			}
		})
	}
}

func TestReadyTimestampDriftDoesNotResetUnknownReproduction(t *testing.T) {
	ref := serving.NodeReference{Name: "node", UID: "node-uid"}
	node := &core.Node{Status: core.NodeStatus{Conditions: []core.NodeCondition{{Type: core.NodeReady, Status: core.ConditionTrue, LastTransitionTime: meta.Unix(1, 0)}}}}
	wanted := serving.SchedulerInstance{Node: ref, Profile: &serving.Profile{ID: "profile"}, Allocation: serving.SchedulerAllocation{MemoryBytes: 1024}}
	observed, failed := ClassifyPreparationFailure(nil, node, nil, errors.New("unclassified start failure"), true)
	if !failed {
		t.Fatal("unknown startup failure was lost")
	}
	first := serving.Action{ID: "first", Node: ref, PreparationFailure: serving.NewPreparationFailure(wanted, "pod-one", observed)}
	node.Status.Conditions[0].LastTransitionTime = meta.Unix(2, 0)
	node.Status.Conditions[0].LastHeartbeatTime = meta.Unix(3, 0)
	repeated, failed := ClassifyPreparationFailure(nil, node, nil, errors.New("unclassified start failure"), true)
	if !failed {
		t.Fatal("Ready timestamp drift erased startup evidence")
	}
	second := serving.Action{ID: "second", Node: ref, PreparationFailure: serving.NewPreparationFailure(wanted, "pod-two", repeated)}
	serving.ObserveRepeatedPreparationFailure(&second, first)
	if second.PreparationFailure.Classification != serving.PreparationPromoted ||
		!second.PreparationFailure.Matches(serving.SchedulerNode{Node: ref, MemoryBytes: 8192, ObservedAt: meta.Unix(4, 0).Time}, wanted) {
		t.Fatal("observation timestamps or allocatable reset an independently reproduced unknown failure")
	}
}
