package kube

import (
	"context"
	"testing"
	"time"

	batch "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTerminalRootHandbackDeletesProfilingLoadsAcrossMeasurementNamespaces(t *testing.T) {
	h := newAdmissionHarness(t)
	h.controller.mu.Lock()
	h.controller.config.MeasurementNamespaces = []string{"chill-system", "profiling"}
	h.controller.config.LoadGenerator.Namespace = "load-system"
	h.controller.mu.Unlock()
	root := baselineRoot("idle", time.Now())
	h.create(root)
	h.admit()
	jobs := []*batch.Job{
		{ObjectMeta: meta.ObjectMeta{Namespace: "profiling", Name: "profiling-load", Labels: map[string]string{loadNodeLabel: "node-uid", "chill.dacs.io/profiling-load": "run-uid"}}},
		{ObjectMeta: meta.ObjectMeta{Namespace: "load-system", Name: "occupancy-load", Labels: map[string]string{loadNodeLabel: "node-uid"}}},
		{ObjectMeta: meta.ObjectMeta{Namespace: "profiling", Name: "another-node-load", Labels: map[string]string{loadNodeLabel: "other-node-uid", "chill.dacs.io/profiling-load": "other-run"}}},
		{ObjectMeta: meta.ObjectMeta{Namespace: "unmanaged", Name: "unmanaged-load", Labels: map[string]string{loadNodeLabel: "node-uid", "chill.dacs.io/profiling-load": "unmanaged-run"}}},
	}
	ctx := context.Background()
	for _, job := range jobs {
		if _, err := h.kube.BatchV1().Jobs(job.Namespace).Create(ctx, job, meta.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	h.complete(root)
	h.admit()
	if h.holder() != "" {
		t.Fatal("completed root did not hand back its Node")
	}
	for i, job := range jobs {
		_, err := h.kube.BatchV1().Jobs(job.Namespace).Get(ctx, job.Name, meta.GetOptions{})
		if i < 2 && !apierrors.IsNotFound(err) {
			t.Fatalf("handback leaked load %s/%s: %v", job.Namespace, job.Name, err)
		}
		if i >= 2 && err != nil {
			t.Fatalf("handback removed unrelated load %s/%s: %v", job.Namespace, job.Name, err)
		}
	}
}
