package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
	actuatorcontroller "github.com/lab-paper-code/chill/internal/nodeactuator/controller"
	"github.com/lab-paper-code/chill/internal/nodeactuator/jetson"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func setupHandoffController(ctx context.Context, manager ctrl.Manager, opts options) error {
	node := &corev1.Node{}
	if err := manager.GetAPIReader().Get(ctx, client.ObjectKey{Name: opts.nodeName}, node); err != nil {
		return fmt.Errorf("resolve assigned Node %q: %w", opts.nodeName, err)
	}
	if node.UID == "" {
		return errors.New("assigned Node has incomplete API identity")
	}
	assigned := nodeactuator.ObjectIdentity{Name: node.Name, UID: string(node.UID)}
	leases, err := actuatorcontroller.NewNodeLeaseManager(manager.GetClient(), actuatorcontroller.LeaseOptions{
		Namespace: opts.leaseNamespace, DurationSeconds: int32(opts.leaseDuration),
	})
	if err != nil {
		return fmt.Errorf("construct Node Lease manager: %w", err)
	}
	safety, err := actuatorcontroller.NewKubernetesWorkloadSafetyChecker(
		manager.GetAPIReader(),
		actuatorcontroller.WorkloadSafetyOptions{
			AllowedNamespaces: append([]string(nil), opts.allowedNamespaces...),
			AllowedPodUIDs:    []string{opts.podUID},
		},
	)
	if err != nil {
		return fmt.Errorf("construct workload safety checker: %w", err)
	}
	hostActuator, err := jetson.NewNVPModel(jetson.NVPModelOptions{
		Command: append([]string(nil), opts.jetsonCommand...), Timeout: opts.jetsonTimeout,
	})
	if err != nil {
		return fmt.Errorf("construct Jetson NVPModel adapter: %w", err)
	}
	reconciler, err := actuatorcontroller.NewHandoffReconciler(
		manager.GetClient(), leases, safety, hostActuator,
		actuatorcontroller.HandoffReconcilerOptions{
			AssignedNode:    assigned,
			AllowedPolicies: append([]nodeactuator.ActuationPolicySpec(nil), opts.allowedPolicies...),
			SafetyValidFor:  opts.safetyValidFor,
		},
	)
	if err != nil {
		return fmt.Errorf("construct NodeActuation reconciler: %w", err)
	}
	if err := reconciler.SetupWithManager(manager); err != nil {
		return fmt.Errorf("set up NodeActuation reconciler: %w", err)
	}
	return nil
}
