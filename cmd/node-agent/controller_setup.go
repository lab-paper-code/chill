package main

import (
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
	actuatorcontroller "github.com/lab-paper-code/chill/internal/nodeactuator/controller"
	"github.com/lab-paper-code/chill/internal/nodeactuator/jetson"
	"github.com/lab-paper-code/chill/internal/nodeactuator/nvidia"
	ctrl "sigs.k8s.io/controller-runtime"
)

func setupPowerModeController(manager ctrl.Manager, opts options) error {
	var hostActuator actuatorcontroller.HostActuator
	timeout := opts.jetsonTimeout
	if len(opts.nvidiaCommand) != 0 {
		clocks, err := parseNvidiaClocks(opts.nvidiaClocks)
		if err != nil {
			return err
		}
		adapter, err := nvidia.New(nvidia.Options{Command: opts.nvidiaCommand, Timeout: opts.nvidiaTimeout, Clocks: clocks})
		if err != nil {
			return fmt.Errorf("construct NVIDIA clock adapter: %w", err)
		}
		hostActuator, timeout = adapter, opts.nvidiaTimeout
	} else {
		adapter, err := jetson.NewNVPModel(jetson.NVPModelOptions{Command: opts.jetsonCommand, Timeout: opts.jetsonTimeout})
		if err != nil {
			return fmt.Errorf("construct Jetson NVPModel adapter: %w", err)
		}
		hostActuator = adapter
	}
	reconciler := &actuatorcontroller.PowerModeReconciler{
		Client: manager.GetClient(), Reader: manager.GetAPIReader(), Host: hostActuator, Node: opts.nodeName,
		Policies: append([]nodeactuator.ActuationPolicySpec(nil), opts.allowedPolicies...),
		Interval: 10 * time.Second, Timeout: timeout, Now: time.Now,
	}
	if err := reconciler.SetupWithManager(manager); err != nil {
		return fmt.Errorf("set up power-mode reconciler: %w", err)
	}
	return nil
}
