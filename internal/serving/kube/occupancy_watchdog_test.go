package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The fake API ignores canceled contexts; this wrapper enforces the real
// status writer's cancellation contract and exposes the execution watchdog.
type occupancyContextClient struct {
	client.Client
	measuring chan context.Context
}

func (c occupancyContextClient) Status() client.SubResourceWriter {
	return occupancyContextStatus{SubResourceWriter: c.Client.Status(), measuring: c.measuring}
}

type occupancyContextStatus struct {
	client.SubResourceWriter
	measuring chan context.Context
}

func (s occupancyContextStatus) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m, ok := object.(*edge.DispatchOccupancyMeasurement); ok && m.Status.Phase == edge.MeasurementPhaseMeasuring {
		s.measuring <- ctx
	}
	return s.SubResourceWriter.Patch(ctx, object, patch, opts...)
}

func TestOccupancyWatchdogBeforeLoadPersistsFailedStatus(t *testing.T) {
	o := occupancyHarness(t)
	m := occupancyRoot("watchdog", time.Now(), 1)
	// Exercise the run watchdog directly, without waiting for a full campaign.
	m.Spec.PlannedElapsedSeconds = 1
	o.create(m)
	measuring := make(chan context.Context, 1)
	o.controller.store.client = occupancyContextClient{Client: o.controller.store.client, measuring: measuring}
	// Expire after Measuring is stored but before preparation or Job creation.
	o.kube.PrependReactor("get", "nodes", func(ktesting.Action) (bool, kruntime.Object, error) {
		ctx := <-measuring
		<-ctx.Done()
		return true, nil, ctx.Err()
	})
	profiles := o.controller.observer.Profiles()
	plan := occupancyPlan{profile: &profiles[0], workload: &edge.Workload{}, powered: true}
	err := o.controller.runOccupancy(context.Background(), m, testNode, plan, o.controller.config)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("watchdog error: %v", err)
	}
	var stored edge.DispatchOccupancyMeasurement
	if err := o.controller.store.client.Get(context.Background(), client.ObjectKeyFromObject(m), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != edge.MeasurementPhaseFailed || !strings.Contains(stored.Status.Reason, context.DeadlineExceeded.Error()) {
		t.Fatalf("expired execution left nonterminal status: %+v", stored.Status)
	}
	jobs, err := o.kube.BatchV1().Jobs(o.controller.config.LoadGenerator.Namespace).List(context.Background(), meta.ListOptions{})
	if err != nil || len(jobs.Items) != 0 {
		t.Fatalf("load started after watchdog expiry: jobs=%+v err=%v", jobs, err)
	}
}
