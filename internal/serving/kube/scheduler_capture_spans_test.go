package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestSchedulerTickReusesRefreshListsAndRecordsCaptureSpans(t *testing.T) {
	const nodesResource = "nodes"
	for _, cluster := range []bool{false, true} {
		name := "namespace-observer"
		if cluster {
			name = "cluster-observer"
		}
		t.Run(name, func(t *testing.T) {
			controller := schedulerEmptyPoolController(t)
			if cluster {
				controller.observer.config.OnCluster = func(observation.ClusterObservation) error { return nil }
			}
			kube := controller.kube.(*kubefake.Clientset)
			var nodesRequestedAt time.Time
			var listElapsed time.Duration
			kube.PrependReactor(listVerb, nodesResource, func(ktesting.Action) (bool, runtime.Object, error) {
				nodesRequestedAt = time.Now()
				time.Sleep(2 * time.Millisecond)
				listElapsed += time.Since(nodesRequestedAt)
				return false, nil, nil
			})
			classLists := 0
			api := controller.store.client.(*managerAPI)
			api.Client = interceptor.NewClient(api.Client.(client.WithWatch), interceptor.Funcs{
				List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*edge.DeviceClassList); ok {
						classLists++
						started := time.Now()
						time.Sleep(2 * time.Millisecond)
						listElapsed += time.Since(started)
					}
					return cl.List(ctx, list, opts...)
				},
			})
			for tick := range 2 {
				kube.ClearActions()
				classLists, listElapsed = 0, 0
				at := time.Now().UTC()
				if _, err := controller.Tick(context.Background(), at); err != nil {
					t.Fatal(err)
				}
				nodeLists := 0
				for _, action := range kube.Actions() {
					if action.GetVerb() == listVerb && action.GetResource().Resource == nodesResource {
						nodeLists++
					}
				}
				if nodeLists != 1 || classLists != 1 {
					t.Fatalf("tick %d repeated live Lists: Nodes=%d DeviceClasses=%d", tick, nodeLists, classLists)
				}
				for _, node := range controller.State().Snapshot.Nodes {
					if node.ObservedAt.IsZero() || node.ObservedAt.After(nodesRequestedAt) {
						t.Fatalf("Node observation was restamped after its List began: %s > %s", node.ObservedAt, nodesRequestedAt)
					}
				}
				for _, summary := range []bool{false, true} {
					rows, _, _, _, err := controller.observationLog.SchedulerObservationPage(context.Background(), at, time.Now().Add(time.Second), 100, 0, observation.SchedulerObservationSelection{Summary: summary})
					if err != nil || len(rows) != 1 || rows[0].Control == nil {
						t.Fatalf("capture completion unavailable: %+v %v", rows, err)
					}
					event := rows[0].Control
					assertRefreshListSpans(t, event)
					if event.Window <= 0 || event.RefreshList < listElapsed || event.SourceProbe < 0 || event.Rest < 0 ||
						event.Window+event.RefreshList+event.SourceProbe+event.Rest != event.Capture {
						t.Fatalf("capture sub-spans overlap, omit live I/O, or exceed capture: %+v; List duration=%s", event, listElapsed)
					}
				}
			}
		})
	}
}

func assertRefreshListSpans(t *testing.T, event *observation.SchedulerControlObservation) {
	t.Helper()
	if event.RefreshNodes <= 0 || event.RefreshPods <= 0 || event.RefreshResources <= 0 ||
		event.RefreshDeviceClasses <= 0 || event.RefreshServices <= 0 {
		t.Fatalf("missing per-List timing: %+v", event)
	}
	total := event.RefreshNodes + event.RefreshPods + event.RefreshResources + event.RefreshDeviceClasses + event.RefreshServices
	if total > event.RefreshList {
		t.Fatalf("per-List spans exceed refresh total: %s > %s", total, event.RefreshList)
	}
}

func TestSchedulerContinueRecordsCaptureSpans(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failed-node-list"
		}
		t.Run(name, func(t *testing.T) {
			controller := schedulerEmptyPoolController(t)
			if fail {
				controller.kube.(*kubefake.Clientset).PrependReactor(listVerb, "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("node List failed")
				})
			}
			at := time.Now().UTC()
			err := controller.Continue(context.Background(), at)
			if (err != nil) != fail {
				t.Fatalf("Continue error = %v, want failure %t", err, fail)
			}
			for _, summary := range []bool{false, true} {
				rows, _, _, _, err := controller.observationLog.SchedulerObservationPage(context.Background(), at, time.Now().Add(time.Second), 100, 0, observation.SchedulerObservationSelection{Summary: summary})
				if err != nil || len(rows) != 1 || rows[0].Control == nil {
					t.Fatalf("Continue completion unavailable: %+v %v", rows, err)
				}
				event := rows[0].Control
				if event.Scope != "continue" || !event.TickAt.Equal(at) || event.Status != "" ||
					event.Evaluate != 0 || event.Window <= 0 || event.RefreshNodes <= 0 ||
					event.Rest < 0 || event.Window+event.RefreshList+event.SourceProbe+event.Rest != event.Capture {
					t.Fatalf("invalid Continue capture record: %+v", event)
				}
				if fail {
					if event.Error == "" || event.RefreshPods != 0 || event.RefreshResources != 0 ||
						event.RefreshDeviceClasses != 0 || event.RefreshServices != 0 || event.RefreshNodes != event.RefreshList {
						t.Fatalf("failed Continue recorded unexecuted Lists: %+v", event)
					}
				} else {
					assertRefreshListSpans(t, event)
					if event.Error != "" || event.Publish <= 0 || event.Launch <= 0 {
						t.Fatalf("successful Continue lost completion spans: %+v", event)
					}
				}
			}
		})
	}
}
