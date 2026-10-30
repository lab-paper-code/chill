package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
)

func run(ctx context.Context, opts options) (retErr error) {
	if opts.materializeProfile {
		return materializeProfile(ctx, opts)
	}
	started := time.Now().UTC()
	podUID, podNamespace, podName := os.Getenv("POD_UID"), os.Getenv("POD_NAMESPACE"), os.Getenv("POD_NAME")
	if podUID == "" || podNamespace == "" || podName == "" {
		return errors.New("POD_UID, POD_NAMESPACE and POD_NAME Downward API identity are required")
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
	observationOwner, err := serving.AcquireObservationOwnership(opts.observationDirectory, ctrl.Log.Info)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, observationOwner.Close()) }()
	removeStagedObservationArchives()
	diagnostics, err := newDiagnosticSession(ctx, opts)
	if err != nil {
		return fmt.Errorf("initialize finite diagnostics: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, diagnostics.Close()) }()
	var recorder *serving.Diagnostics
	if diagnostics != nil {
		recorder = diagnostics.recorder
	}
	publisher := newLivePublisher(ctx)
	observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: opts.observationDirectory, Store: opts.storeName,
		Writer: objects, Definitions: opts.definitions, Outcomes: opts.outcomes,
		Diagnostics:    recorder,
		Retention:      opts.observationRetention,
		OnRequestEvent: publisher.request,
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
	if err := recordCluster(observationLog, publisher, serverStartCluster(podUID, podNamespace, podName)); err != nil {
		return fmt.Errorf("record server Pod identity: %w", err)
	}
	selectedPod := func(pod *corev1.Pod) bool {
		namespace := podNamespace
		if pod.Labels["app.kubernetes.io/component"] == "serving-instance" {
			namespace = opts.servingNamespace
		}
		return resourcePodSelected(pod, namespace, opts.resourceRelease, opts.resourceControlComponents) ||
			resourcePodSelected(pod, namespace, opts.resourceSystem, opts.resourceControlComponents)
	}
	process := newProcessResources(
		observationLog, opts.observationDirectory, podUID, started, opts.resourceDirectoryInterval,
	)
	publisher.recordResources = process.record
	// Backend lifetime belongs to ChillServer, not SIGTERM or an HTTP caller.
	backendCtx, cancelBackend := context.WithCancel(context.Background())
	defer cancelBackend()
	execution, err := serving.NewRuntime(backendCtx, serving.RuntimeConfig{
		QueueLimit: opts.queueLimit, ObservationLog: observationLog,
		Diagnostics: recorder, OnChange: publisher.changedRuntime,
	})
	if err != nil {
		return err
	}
	// One idle connection per in-flight request of an instance (e1 maxQueueSize
	// 256) keeps batch completions on keep-alive; 30 s closes before the
	// serving-worker's 60 s IdleTimeout.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns, transport.MaxIdleConnsPerHost, transport.IdleConnTimeout = 0, 256, 30*time.Second
	native := &firstfloor.Client{HTTPClient: &http.Client{Transport: transport}, Diagnostics: recorder}
	observer, err := servingkube.NewObserver(store, kube, execution, native, servingkube.ObserverConfig{
		Namespace:        opts.servingNamespace,
		OnCluster:        func(row serving.ClusterObservation) error { return recordCluster(observationLog, publisher, row) },
		SelectClusterPod: selectedPod,
	})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, observer.Close()) }()
	manager, err := servingkube.NewManager(store, execution, observationLog, observer,
		servingkube.ManagerConfig{DrainTimeout: opts.drainTimeout, OnWorkload: publisher.workload})
	if err != nil {
		return err
	}
	handlerConfig := httpapi.HandlerConfig{
		MaxRequestBytes: opts.maxRequestBytes, RequestTimeout: opts.requestTimeout, OperationTimeout: opts.operationTimeout,
		MaxQueryEvents: opts.maxQueryEvents,
		Live:           publisher.hub,
		ReceiveResources: (&resourceReceiver{
			kube: kube, log: observationLog, namespace: corev1.NamespaceAll, selectPod: selectedPod,
			maxSamples: opts.resourceMaxSamples, maxContainers: opts.resourceMaxContainers,
			maxThermal: opts.resourceMaxThermal, maxErrors: opts.resourceMaxErrors,
		}).receive,
	}
	scheduler, err := newSchedulerService(
		ctx, store, kube, execution, observer, objects, observationLog, opts,
		handlerConfig.RequestTimeout, publisher,
	)
	if err != nil {
		return fmt.Errorf("initialize scheduler service: %w", err)
	}
	defer scheduler.Close()
	if err := initializeServing(ctx, &opts, observer, manager, execution,
		publisher, scheduler, &handlerConfig); err != nil {
		return err
	}
	// HTTP-boundary calls (reviews, settings ConfigMap) take their own client
	// budget so ingress and control calls never queue behind each other.
	reviews, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	authorizer := &servingkube.Authorizer{Client: reviews}
	measurements, err := newMeasurementService(store.Client(), kube, scheduler, objects, opts)
	if err != nil {
		return err
	}
	handlerConfig.ListMeasurements = measurements.list
	handlerConfig.SubmitMeasurement = measurements.submit
	handlerConfig.DeleteMeasurement = measurements.delete
	handlerConfig.ListOperationCostMeasurements = measurements.listOperationCosts
	handlerConfig.GetOperationCostMeasurement = measurements.getOperationCost
	handlerConfig.ListDispatchOccupancyMeasurements = measurements.listDispatchOccupancies
	handlerConfig.GetDispatchOccupancyMeasurement = measurements.getDispatchOccupancy
	experiments := &experimentService{
		reader: objects, lister: objects, store: opts.storeName, maxBytes: opts.maxRequestBytes,
	}
	handlerConfig.ListExperiments = experiments.list
	handlerConfig.GetExperiment = experiments.get
	handler, err := httpapi.NewHandler(manager, execution, authorizer, observationLog, observer, handlerConfig)
	if err != nil {
		return err
	}
	chillServer, err := composeChillServer(handler, authorizer, opts)
	if err != nil {
		return err
	}
	return serve(ctx, opts, chillServer, execution, observer, manager, observationLog, scheduler, diagnostics)
}

func removeStagedObservationArchives() {
	// An upload interrupted by a restart leaves its staging files on scratch space.
	for _, pattern := range []string{".archive-*", "chill-evidence-*"} {
		staged, _ := filepath.Glob(filepath.Join(os.TempDir(), pattern))
		for _, name := range staged {
			_ = os.Remove(name)
		}
	}
}

func initializeServing(ctx context.Context, opts *options,
	observer *servingkube.Observer, manager *servingkube.Manager, execution *serving.Runtime,
	publisher *livePublisher, scheduler *schedulerService,
	handlerConfig *httpapi.HandlerConfig,
) error {
	initializeCtx, cancelInitialize := context.WithTimeout(ctx, opts.requestTimeout)
	err := observer.Refresh(initializeCtx)
	if err == nil {
		err = manager.Initialize(initializeCtx)
	}
	cancelInitialize()
	if err != nil {
		return fmt.Errorf("initialize Workload service: %w", err)
	}
	views, err := manager.ListWorkloads(ctx)
	if err != nil {
		return fmt.Errorf("initialize live Workload projection: %w", err)
	}
	for _, view := range views {
		publisher.workload(view, false)
	}
	publisher.startRuntime(ctx, execution)
	if scheduler != nil && len(scheduler.controller.Settings().AutonomousWorkloads) > 0 {
		execution.EnableSchedulerRouting()
	}
	if scheduler != nil {
		handlerConfig.Scheduler = scheduler.controller
		handlerConfig.SchedulerConfigMap = opts.schedulerConfigMap
		handlerConfig.SchedulerConfigKey = opts.schedulerConfigKey
		handlerConfig.SchedulerNamespace = opts.servingNamespace
		handlerConfig.SchedulerLock = &scheduler.lock
		handlerConfig.RefreshScheduler = scheduler.refreshLocked
		handlerConfig.SchedulerMeters = scheduler.schedulerMeters
		handlerConfig.OperatingPower = scheduler.operatingPower
		handlerConfig.Profiles = scheduler.profilesView
	}
	return nil
}

func serve(ctx context.Context, opts options, chillServer http.Handler, execution *serving.Runtime,
	observer *servingkube.Observer, manager *servingkube.Manager,
	observationLog *serving.ObservationLog, scheduler *schedulerService, diagnostics *diagnosticSession,
) error {
	server := &http.Server{
		Addr: opts.bindAddress, Handler: chillServer,
		ReadHeaderTimeout: opts.requestTimeout, IdleTimeout: opts.requestTimeout,
	}
	serverErrors := make(chan error, 2)
	listener, err := net.Listen("tcp", opts.bindAddress)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	closePprof, err := startPprof(opts, serverErrors)
	if err != nil {
		return err
	}
	defer closePprof()
	go func() { serverErrors <- server.ServeTLS(listener, opts.tlsCert, opts.tlsKey) }()
	diagnostics.listenerBound()
	scheduler.Start()
	sourceTicker := time.NewTicker(opts.sourceInterval)
	defer sourceTicker.Stop()
	profileTicker := time.NewTicker(opts.profileInterval)
	defer profileTicker.Stop()
	archiveTicker := time.NewTicker(time.Minute)
	defer archiveTicker.Stop()
	archiving := make(chan struct{}, 1)
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
			cancel()
			if sourceErr != nil {
				ctrl.Log.Error(sourceErr, "refresh current native serving facts")
			}
			// A slow refresh does not spend the Workload advance's bound.
			callCtx, cancel = context.WithTimeout(ctx, opts.requestTimeout)
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
			archiveInBackground(ctx, archiving, opts.requestTimeout, observationLog.Flush)
		}
	}
	archiving <- struct{}{} // The running Flush ends before Close archives.
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

// archiveInBackground starts one Flush unless one still runs, so source
// refresh and Advance keep their ticks while an hour uploads.
func archiveInBackground(
	ctx context.Context, running chan struct{}, timeout time.Duration, flush func(context.Context) error,
) {
	select {
	case running <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-running }()
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := flush(callCtx); err != nil {
			ctrl.Log.Error(err, "archive serving observation hours")
		}
	}()
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
