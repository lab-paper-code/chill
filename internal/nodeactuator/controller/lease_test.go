package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNodeLeaseManagerSerializesOneNodeByUIDAndAllowsExpiredTakeover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := leaseTestScheme(t)
	baseClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	kubeClient := &leaseUIDClient{Client: baseClient}
	manager, err := NewNodeLeaseManager(kubeClient, LeaseOptions{
		Namespace: "chill-system", DurationSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	firstRequest := leaseTestRequest("handoff-a")
	first, err := manager.AcquireOrRenew(ctx, firstRequest, now)
	if err != nil || first.State != nodeactuator.LockHeld || first.RetryAfter != 10*time.Second || first.UID == "" {
		t.Fatalf("first acquire = %#v, error=%v", first, err)
	}
	lease, key := requireCreatedNodeLease(t, ctx, kubeClient, firstRequest)
	observed, err := manager.Observe(ctx, firstRequest, now)
	if err != nil || observed.State != nodeactuator.LockHeld || observed.RetryAfter != 10*time.Second {
		t.Fatalf("held observation = %#v, error=%v", observed, err)
	}

	secondRequest := leaseTestRequest("handoff-b")
	conflict, err := manager.AcquireOrRenew(ctx, secondRequest, now.Add(5*time.Second))
	if err != nil || conflict.State != nodeactuator.LockConflict || conflict.RetryAfter != 25*time.Second ||
		conflict.HolderIdentity != first.HolderIdentity {
		t.Fatalf("conflict = %#v, error=%v", conflict, err)
	}
	observed, err = manager.Observe(ctx, secondRequest, now.Add(5*time.Second))
	if err != nil || observed.State != nodeactuator.LockConflict || observed.RetryAfter != 25*time.Second {
		t.Fatalf("conflict observation = %#v, error=%v", observed, err)
	}
	observed, err = manager.Observe(ctx, firstRequest, now.Add(31*time.Second))
	if err != nil || observed.State != nodeactuator.LockLost {
		t.Fatalf("expired holder observation = %#v, error=%v", observed, err)
	}
	taken, err := manager.AcquireOrRenew(ctx, secondRequest, now.Add(31*time.Second))
	if err != nil || taken.State != nodeactuator.LockHeld || taken.HolderIdentity == first.HolderIdentity {
		t.Fatalf("expired takeover = %#v, error=%v", taken, err)
	}
	if err := kubeClient.Get(ctx, key, lease); err != nil {
		t.Fatal(err)
	}
	if lease.Spec.LeaseTransitions == nil || *lease.Spec.LeaseTransitions != 1 ||
		lease.Annotations[leaseHandoffUIDAnnotation] != secondRequest.Handoff.UID {
		t.Fatalf("taken Lease = %#v", lease.Spec)
	}
	observed, err = manager.Observe(ctx, secondRequest, now.Add(31*time.Second))
	if err != nil || observed.State != nodeactuator.LockHeld || observed.HolderIdentity != taken.HolderIdentity {
		t.Fatalf("takeover observation = %#v, error=%v", observed, err)
	}
}

func TestNodeLeaseManagerReleasesOnlyExactHolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := leaseTestScheme(t)
	baseClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	kubeClient := &leaseUIDClient{Client: baseClient}
	manager, err := NewNodeLeaseManager(kubeClient, LeaseOptions{
		Namespace: "chill-system", DurationSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	firstRequest := leaseTestRequest("handoff-a")
	if _, err := manager.AcquireOrRenew(ctx, firstRequest, now); err != nil {
		t.Fatal(err)
	}
	secondRequest := leaseTestRequest("handoff-b")
	if _, err := manager.AcquireOrRenew(ctx, secondRequest, now.Add(31*time.Second)); err != nil {
		t.Fatal(err)
	}
	if released, err := manager.Release(ctx, firstRequest); err == nil || released ||
		!strings.Contains(err.Error(), "another handoff") {
		t.Fatalf("foreign release = %t, error=%v", released, err)
	}
	if released, err := manager.Release(ctx, secondRequest); err != nil || !released {
		t.Fatalf("holder release = %t, error=%v", released, err)
	}
	key := client.ObjectKey{Namespace: "chill-system", Name: nodeLeaseName(firstRequest.Node.UID)}
	lease := &coordinationv1.Lease{}
	if err := kubeClient.Get(ctx, key, lease); !apierrors.IsNotFound(err) {
		t.Fatalf("released Lease still exists: %v", err)
	}
}

func TestNodeLeaseManagerRenewsOnlyExactHolderAndRejectsNodeIdentityDrift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scheme := leaseTestScheme(t)
	baseClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	kubeClient := &leaseUIDClient{Client: baseClient}
	manager, err := NewNodeLeaseManager(kubeClient, LeaseOptions{
		Namespace: "chill-system", DurationSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	request := leaseTestRequest("handoff-a")
	if _, err := manager.AcquireOrRenew(ctx, request, now); err != nil {
		t.Fatal(err)
	}
	renewed, err := manager.AcquireOrRenew(ctx, request, now.Add(5*time.Second))
	if err != nil || renewed.State != nodeactuator.LockHeld {
		t.Fatalf("renew = %#v, error=%v", renewed, err)
	}
	lease := &coordinationv1.Lease{}
	key := client.ObjectKey{Namespace: "chill-system", Name: nodeLeaseName(request.Node.UID)}
	if err := kubeClient.Get(ctx, key, lease); err != nil {
		t.Fatal(err)
	}
	lease.Annotations[leaseNodeUIDAnnotation] = "other-node"
	if err := kubeClient.Update(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AcquireOrRenew(ctx, request, now.Add(6*time.Second)); err == nil ||
		!strings.Contains(err.Error(), "Node identity") {
		t.Fatalf("identity drift error = %v", err)
	}
}

func TestLeaseOptionsRejectsUnboundedOrImplicitConfiguration(t *testing.T) {
	t.Parallel()
	for _, options := range []LeaseOptions{
		{},
		{Namespace: " chill-system", DurationSeconds: 30},
		{Namespace: "chill-system", DurationSeconds: 4},
		{Namespace: "chill-system", DurationSeconds: 301},
	} {
		if err := options.Validate(); err == nil {
			t.Fatalf("invalid Lease options accepted: %#v", options)
		}
	}
}

func leaseTestRequest(handoffUID string) nodeactuator.Request {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	return nodeactuator.Request{
		Handoff: nodeactuator.ObjectIdentity{Name: handoffUID, UID: handoffUID}, Generation: 1,
		Node:        nodeactuator.ObjectIdentity{Name: "jetsonx", UID: "node-uid"},
		DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
		Target: nodeactuator.Target{
			Kind:           nodeactuator.ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &nodeactuator.JetsonNVPModelTarget{ID: 2},
		},
		PolicyName:              "actuation-policy-v1",
		SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		Deadline:         now.Add(15 * time.Minute),
		StageRequestedAt: now,
	}
}

func requireCreatedNodeLease(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	request nodeactuator.Request,
) (*coordinationv1.Lease, client.ObjectKey) {
	t.Helper()
	lease := &coordinationv1.Lease{}
	key := client.ObjectKey{Namespace: "chill-system", Name: nodeLeaseName(request.Node.UID)}
	if err := kubeClient.Get(ctx, key, lease); err != nil {
		t.Fatal(err)
	}
	if len(lease.OwnerReferences) != 0 || lease.Annotations[leaseNodeUIDAnnotation] != request.Node.UID ||
		lease.Annotations[leaseHandoffUIDAnnotation] != request.Handoff.UID ||
		lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != nodeLeaseHolder(request.Handoff.UID) {
		t.Fatalf("created Lease = %#v", lease)
	}
	return lease, key
}

type leaseUIDClient struct {
	client.Client
}

func (c *leaseUIDClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	if lease, ok := object.(*coordinationv1.Lease); ok && lease.UID == "" {
		lease.UID = types.UID("lease-uid")
	}
	return c.Client.Create(ctx, object, opts...)
}

func leaseTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
