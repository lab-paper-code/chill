package kube

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Incident revision discarded the same-input failure after its observation
// window; ordinary process exits and OOM therefore kept being prepared again.
func TestPrepareFailureSurvivesObservationWindow(t *testing.T) {
	const unknownFailure = "Unrecognized"
	const crashLoop = "CrashLoopBackOff"
	const imagePull = "ErrImagePull"
	const imagePullBackoff = "ImagePullBackOff"
	for _, failure := range []string{"OOMKilled", "Error", "Completed", imagePull, imagePullBackoff, crashLoop, unknownFailure} {
		t.Run(failure, func(t *testing.T) {
			c, prepared := tickReadySubsetController(t, nil)
			prepared.Store(true)
			ctx := context.Background()
			snapshot, err := c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			wanted := snapshot.Instances[0]
			before, err := c.operations.Capture(ctx, wanted.Node)
			if err != nil {
				t.Fatal(err)
			}
			pods, err := c.kube.CoreV1().Pods("chill-system").List(ctx, meta.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			index := slices.IndexFunc(pods.Items, func(p core.Pod) bool { return string(p.UID) == wanted.PodUID })
			if index < 0 {
				t.Fatal("fixture Pod missing")
			}
			pod := pods.Items[index].DeepCopy()
			status := &pod.Status.ContainerStatuses[0]
			status.Ready = false
			switch failure {
			case unknownFailure:
				pod.Status.Phase, pod.Status.Reason = core.PodFailed, unknownFailure
				pod.Status.ContainerStatuses = nil
			case imagePull, imagePullBackoff, crashLoop:
				pod.Status.Phase = core.PodPending
				status.State = core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: failure}}
				if failure == crashLoop {
					status.LastTerminationState = core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: "Error", ExitCode: 1, ContainerID: status.ContainerID, StartedAt: meta.NewTime(time.Now())}}
				}
			default:
				code := int32(1)
				if failure == "Completed" {
					code = 0
				}
				pod.Status.Phase = core.PodFailed
				status.State = core.ContainerState{Terminated: &core.ContainerStateTerminated{Reason: failure, ExitCode: code, ContainerID: status.ContainerID, StartedAt: meta.NewTime(time.Now())}}
			}
			if _, err = c.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, meta.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			_, startErr := c.operations.PrepareObserved(ctx, pod)
			if startErr == nil {
				t.Fatal("startup failure did not fail preparation")
			}
			observed, _ := servingstate.ClassifyPreparationFailure(pod, nil, nil, startErr, true)
			at := time.Now()
			result := servingstate.OperationResult{
				Operation: servingstate.Operation{ID: "failed-start", Kind: servingstate.OperationPrepare,
					Node: wanted.Node, Prepare: &servingstate.PrepareSpec{Instance: wanted, MemoryBytes: wanted.Allocation.MemoryBytes}},
				Before: before, StartedAt: at, EndedAt: at, Status: servingstate.NativeOperationFailed, Error: startErr.Error(),
				Preparation: &servingstate.PreparationEvidence{PodUID: wanted.PodUID, NodeUID: wanted.Node.UID, Created: true},
			}
			result.Preparation.Failure = &observed
			if failure == imagePullBackoff {
				result.Status = servingstate.NativeOperationPartial
			}
			run := &actionRun{action: serving.Action{ID: result.Operation.ID, Kind: servingstate.OperationPrepare,
				Node: wanted.Node, To: serving.SchedulerInstanceState(wanted)}}
			c.runs[result.Operation.ID] = run
			c.publishSnapshotLocked(&snapshot)
			c.completeRun(run, &result, true, startErr, ctx)
			snapshot.Options = []serving.SchedulerOption{{Instance: wanted, CompatibleUIDs: []string{wanted.WorkloadUID}}}
			later := at.Add(10 * c.config.Settings.ObservationWindow)
			next, _ := c.reconcileSnapshot(snapshot, later, c.config.Settings.ObservationWindow, c.runtime)
			if !slices.ContainsFunc(next.Actions, func(a serving.Action) bool { return a.ID == result.Operation.ID }) {
				t.Fatal("failed start expired although placement evidence did not change")
			}
			expectedClass := serving.PreparationDeterministic
			if failure == imagePull || failure == imagePullBackoff {
				expectedClass = serving.PreparationTransient
			} else if failure == unknownFailure {
				expectedClass = serving.PreparationUnknownTransient
			}
			if run.action.PreparationFailure == nil || run.action.PreparationFailure.Classification != expectedClass {
				t.Fatalf("startup classification lost: %+v", run.action.PreparationFailure)
			}
			if failure == unknownFailure {
				repeated := result
				repeated.Operation.ID += "-repeat"
				repeated.StartedAt, repeated.EndedAt = at.Add(c.config.Settings.ObservationWindow), at.Add(c.config.Settings.ObservationWindow)
				evidence := *result.Preparation
				evidence.PodUID += "-repeat"
				repeated.Preparation = &evidence
				retry := &actionRun{action: serving.Action{ID: repeated.Operation.ID, Kind: servingstate.OperationPrepare, Node: wanted.Node}}
				c.runs[repeated.Operation.ID] = retry
				c.completeRun(retry, &repeated, true, startErr, ctx)
				next, _ = c.reconcileSnapshot(snapshot, later, c.config.Settings.ObservationWindow, c.runtime)
				if !slices.ContainsFunc(next.Actions, func(a serving.Action) bool {
					return a.ID == repeated.Operation.ID && a.PreparationFailure.Classification == serving.PreparationPromoted &&
						a.PreparationFailure.PreviousPodUID == wanted.PodUID && a.PreparationFailure.RepeatCount == 1
				}) {
					t.Fatal("same-input independent failure was not promoted in the scheduler API facts")
				}
			}
			expectedReason := failure
			if failure == crashLoop {
				expectedReason = "Error"
			}
			if !strings.Contains(startErr.Error(), expectedReason) {
				t.Fatalf("physical failure reason lost: %v", startErr)
			}
			profile := *wanted.Profile
			profile.Batches = slices.Clone(profile.Batches)
			for i := range profile.Batches {
				profile.Batches[i].MemoryBytes++
			}
			snapshot.Options[0].Instance.Profile = &profile
			next, _ = c.reconcileSnapshot(snapshot, later.Add(c.config.Settings.ObservationWindow), c.config.Settings.ObservationWindow, c.runtime)
			if slices.ContainsFunc(next.Actions, func(a serving.Action) bool { return a.ID == result.Operation.ID }) {
				t.Fatal("changed memory input retained obsolete start failure")
			}
		})
	}
}
