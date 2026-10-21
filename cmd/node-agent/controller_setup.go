package main

import (
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
	actuatorcontroller "github.com/lab-paper-code/chill/internal/nodeactuator/controller"
	"github.com/lab-paper-code/chill/internal/nodeactuator/jetson"
	ctrl "sigs.k8s.io/controller-runtime"
)

func setupPowerModeController(manager ctrl.Manager, opts options) error {
	hostActuator, err := jetson.NewNVPModel(jetson.NVPModelOptions{
		Command: append([]string(nil), opts.jetsonCommand...), Timeout: opts.jetsonTimeout,
	})
	if err != nil {
		return fmt.Errorf("construct Jetson NVPModel adapter: %w", err)
	}
	reconciler := &actuatorcontroller.PowerModeReconciler{
		Client: manager.GetClient(), Reader: manager.GetAPIReader(), Host: hostActuator, Node: opts.nodeName,
		Policies: append([]nodeactuator.ActuationPolicySpec(nil), opts.allowedPolicies...),
		Interval: 10 * time.Second, Timeout: opts.jetsonTimeout, Now: time.Now,
	}
	if err := reconciler.SetupWithManager(manager); err != nil {
		return fmt.Errorf("set up power-mode reconciler: %w", err)
	}
	return nil
}
