package main

import (
	"net/http"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving/application"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/measurement"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func measurementConfig(opts options) measurement.Config {
	return measurement.Config{
		ServingNamespace: opts.servingNamespace, ProfileNamespaces: opts.profileNamespaces,
		SchedulerConfigMap: opts.schedulerConfigMap, SchedulerConfigKey: opts.schedulerConfigKey,
		MaxRequestBytes: opts.maxRequestBytes, StoreName: opts.storeName,
		LoadGeneratorNamespace: opts.loadGenerator.Namespace,
	}
}

func newMeasurementService(native client.Client, kube kubernetes.Interface, scheduler *application.SchedulerService,
	objects measurement.Objects, opts options,
) (*measurement.Service, error) {
	origin, err := privateOrigin(opts.queryURL)
	if err != nil {
		return nil, err
	}
	var inputs *measurement.SchedulerInputs
	if scheduler != nil {
		inputs = &measurement.SchedulerInputs{
			Lock: &scheduler.Lock, RefreshLock: &scheduler.RefreshLock, GatewayRequestTimeout: scheduler.GatewayRequestTimeout,
			DecodePolicy: func(raw string, maxBytes int64) (*measurement.DispatchOccupancyPolicy, error) {
				config, err := application.DecodeSchedulerConfiguration(raw, maxBytes)
				return config.DispatchOccupancy, err
			},
			LoadProfiles: func() (string, []serving.Profile, map[string]servingstate.PrepareSpec, bool) {
				snapshot := scheduler.Profiles.Load()
				if snapshot == nil {
					return "", nil, nil, false
				}
				return snapshot.ConfigVersion, snapshot.Profiles, snapshot.Preparations, true
			},
		}
	}
	return measurement.New(native, kube, inputs, objects, measurementConfig(opts), origin, &http.Client{
		Timeout:       opts.requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}), nil
}
