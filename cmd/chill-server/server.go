package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
)

func run(ctx context.Context, opts options) (retErr error) {
	if opts.materializeProfile {
		return materializeProfile(ctx, opts)
	}
	config, err := kubeconfig.BuildConfig(opts.kube)
	if err != nil {
		return err
	}
	config.QPS = float32(opts.kubeAPIQPS)
	config.Burst = opts.kubeAPIBurst
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err = clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err = api.AddToScheme(scheme); err != nil {
		return err
	}
	store, err := servingkube.NewStore(config, scheme)
	if err != nil {
		return err
	}
	objects, err := objectStore(opts)
	if err != nil {
		return err
	}
	observationOwner, err := serving.AcquireObservationOwnership(opts.observationDirectory)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, observationOwner.Close()) }()
	observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: opts.observationDirectory, Store: opts.storeName,
		Writer: objects, Definitions: opts.definitions, Outcomes: opts.outcomes,
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), opts.requestTimeout)
		defer cancel()
		if err := observationLog.Close(closeCtx); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close serving observations: %w", err))
		}
	}()
	// Backend lifetime belongs to ChillServer, not SIGTERM or an HTTP caller.
	backendCtx, cancelBackend := context.WithCancel(context.Background())
	defer cancelBackend()
	execution, err := serving.NewRuntime(backendCtx, serving.RuntimeConfig{
		QueueLimit: opts.queueLimit, ObservationLog: observationLog,
	})
	if err != nil {
		return err
	}
	observer, err := servingkube.NewObserver(store, kube, execution, &firstfloor.Client{}, servingkube.ObserverConfig{
		Namespace: opts.servingNamespace, ActuationNamespace: opts.actuationNamespace,
	})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, observer.Close()) }()
	manager, err := servingkube.NewManager(store, execution, observationLog, observer,
		servingkube.ManagerConfig{DrainTimeout: opts.drainTimeout})
	if err != nil {
		return err
	}
	scheduler, err := newSchedulerService(ctx, store, kube, execution, observer, objects, observationLog, opts)
	if err != nil {
		return fmt.Errorf("initialize scheduler service: %w", err)
	}
	defer scheduler.Close()
	initializeCtx, cancelInitialize := context.WithTimeout(ctx, opts.requestTimeout)
	err = observer.Refresh(initializeCtx)
	if err == nil {
		err = manager.Initialize(initializeCtx)
	}
	cancelInitialize()
	if err != nil {
		return fmt.Errorf("initialize Workload service: %w", err)
	}
	if scheduler != nil && len(scheduler.controller.Settings().AutonomousWorkloads) > 0 {
		execution.EnableSchedulerRouting()
	}
	handlerConfig := httpapi.HandlerConfig{
		MaxRequestBytes: opts.maxRequestBytes, RequestTimeout: opts.requestTimeout,
		MaxQueryEvents: opts.maxQueryEvents,
	}
	if scheduler != nil {
		handlerConfig.Scheduler = scheduler.controller
		handlerConfig.SchedulerConfigMap = opts.schedulerConfigMap
		handlerConfig.SchedulerConfigKey = opts.schedulerConfigKey
		handlerConfig.SchedulerNamespace = opts.servingNamespace
		handlerConfig.SchedulerLock = &scheduler.lock
		handlerConfig.RefreshScheduler = scheduler.refreshLocked
		handlerConfig.OperatingPower = scheduler.operatingPower
		handlerConfig.Profiles = scheduler.profilesView
	}
	authorizer := &servingkube.Authorizer{Client: kube}
	measurements, err := newMeasurementService(store.Client(), kube, scheduler, objects, opts)
	if err != nil {
		return err
	}
	handlerConfig.ListMeasurements = measurements.list
	handlerConfig.SubmitMeasurement = measurements.submit
	handlerConfig.DeleteMeasurement = measurements.delete
	handler, err := httpapi.NewHandler(manager, execution, authorizer, observationLog, observer, handlerConfig)
	if err != nil {
		return err
	}
	chillServer, err := composeChillServer(handler, authorizer, opts)
	if err != nil {
		return err
	}
	return serve(ctx, opts, chillServer, execution, observer, manager, observationLog, scheduler)
}

func serve(ctx context.Context, opts options, chillServer http.Handler, execution *serving.Runtime,
	observer *servingkube.Observer, manager *servingkube.Manager,
	observationLog *serving.ObservationLog, scheduler *schedulerService,
) error {
	server := &http.Server{
		Addr: opts.bindAddress, Handler: chillServer,
		ReadHeaderTimeout: opts.requestTimeout, IdleTimeout: opts.requestTimeout,
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServeTLS(opts.tlsCert, opts.tlsKey) }()
	scheduler.Start()
	sourceTicker := time.NewTicker(opts.sourceInterval)
	defer sourceTicker.Stop()
	profileTicker := time.NewTicker(opts.profileInterval)
	defer profileTicker.Stop()
	archiveTicker := time.NewTicker(time.Minute)
	defer archiveTicker.Stop()
	var serveError error
loop:
	for {
		select {
		case serveError = <-serverErrors:
			break loop
		case <-ctx.Done():
			break loop
		case <-sourceTicker.C:
			callCtx, cancel := context.WithTimeout(ctx, opts.requestTimeout)
			sourceErr := observer.Refresh(callCtx)
			if sourceErr != nil {
				ctrl.Log.Error(sourceErr, "refresh current native serving facts")
			}
			if err := manager.Advance(callCtx); err != nil {
				ctrl.Log.Error(err, "advance recorded Workload operations")
			}
			cancel()
		case <-profileTicker.C:
			callCtx, cancel := context.WithTimeout(ctx, opts.requestTimeout)
			if scheduler != nil {
				if err := scheduler.refresh(callCtx); err != nil {
					ctrl.Log.Error(err, "refresh selected profiling evidence")
				}
			}
			cancel()
		case <-archiveTicker.C:
			callCtx, cancel := context.WithTimeout(ctx, opts.requestTimeout)
			if err := observationLog.Flush(callCtx); err != nil {
				ctrl.Log.Error(err, "archive serving observation hours")
			}
			cancel()
		}
	}
	scheduler.Close()
	// Keep native source watches alive until accepted backend work really drains.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), opts.drainTimeout)
	defer cancelShutdown()
	shutdownError := shutdown(shutdownCtx, server, execution)
	if errors.Is(serveError, http.ErrServerClosed) {
		serveError = nil
	}
	return errors.Join(serveError, shutdownError)
}

func shutdown(ctx context.Context, server *http.Server, execution *serving.Runtime) error {
	drainErrors := make(chan error, 1)
	go func() { drainErrors <- execution.Shutdown(ctx) }()
	shutdownError := server.Shutdown(ctx)
	if shutdownError != nil {
		shutdownError = errors.Join(shutdownError, server.Close())
	}
	return errors.Join(shutdownError, <-drainErrors)
}
