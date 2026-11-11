package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/application"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	evaluation "github.com/lab-paper-code/chill/internal/evaluation/service"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
	"github.com/lab-paper-code/chill/internal/serving/management"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
)

func run(ctx context.Context, opts options) (retErr error) {
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
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
	objects, evaluationObjects, evaluationStoreName, err := objectStores(opts)
	if err != nil {
		return err
	}
	observationOwner, err := observation.AcquireObservationOwnership(opts.observationDirectory, ctrl.Log.Info)
	if err != nil {
		return err
	}
	releaseObservationOwner := true
	defer func() {
		retErr = errors.Join(retErr, closeObservationOwner(observationOwner, releaseObservationOwner))
	}()
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
	var queueLength, queueOldestEnqueued atomic.Int64
	publisher := newLivePublisher(ctx, &queueLength, &queueOldestEnqueued)
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: opts.observationDirectory, Store: opts.storeName,
		Writer: objects, Definitions: opts.definitions, Outcomes: opts.outcomes,
		OccupancyTail:    httpapi.MaximumInferenceRequestTimeout + opts.drainTimeout,
		Diagnostics:      recorder,
		Retention:        opts.observationRetention,
		EventLookupHours: opts.eventLookupHours,
		OnRequestEvent:   publisher.request,
		QueueLength:      &queueLength, QueueOldestEnqueued: &queueOldestEnqueued,
	})
	if err != nil {
		cancelRun()
		publisher.wait()
		return err
	}
	defer func() {
		// Shutdown closes local files but leaves unfinished hours for next startup.
		cancelRun()
		publisher.wait()
		if err := observationLog.Close(context.Background()); err != nil {
			// A failed recording close is not a graceful ownership handoff.
			// Forced process exit leaves the active marker for crash recovery.
			releaseObservationOwner = false
			retErr = errors.Join(retErr, fmt.Errorf("close serving observations: %w", err))
		}
	}()
	startup := serverStartCluster(podUID, podNamespace, podName, started, opts)
	if err := recordCluster(observationLog, publisher, startup); err != nil {
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
		ObservationLog: observationLog,
		Diagnostics:    recorder, OnChange: publisher.changedRuntime,
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
	cluster := servingkube.NewClusterState(store, kube, opts.servingNamespace)
	observer, err := servingkube.NewObserver(cluster, store, kube, execution, native, servingkube.ObserverConfig{
		Namespace: opts.servingNamespace,
		RuntimeImage: func(ctx context.Context, profile *serving.Profile) (string, error) {
			c := profile.Condition
			return profilerkube.ResolveRuntimeImage(ctx, store.Client(), profilerkube.CandidateResolverOptions{
				Namespace: opts.factsNamespace, ConfigMapName: opts.factsConfigMap, ConfigMapKey: opts.executionFactsKey,
			}, c.DeviceClass, profile.NodeName, c.RuntimeFamily, c.Backend)
		},
		OnCluster: func(row observation.ClusterObservation) error {
			return recordCluster(observationLog, publisher, row)
		},
		SelectClusterPod: selectedPod,
	})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, observer.Close()) }()
	manager, err := management.NewManager(cluster, store, execution, observationLog, observer,
		management.ManagerConfig{DrainTimeout: opts.drainTimeout, OnWorkload: publisher.workload,
			Actor: func(ctx context.Context) (string, string) { return servingkube.PrincipalFromContext(ctx).Actor() }})
	if err != nil {
		return err
	}
	handlerConfig := httpapi.HandlerConfig{
		MaxRequestBytes: opts.maxRequestBytes, RequestTimeout: opts.requestTimeout, OperationTimeout: opts.operationTimeout,
		MaxQueryEvents: opts.maxQueryEvents,
		Live:           publisher.hub,
		ReceiveResources: (&resourceReceiver{
			kube: kube, log: observationLog, namespace: corev1.NamespaceAll,
			selectPod: func(pod *corev1.Pod) bool {
				return selectedPod(pod) || loadGeneratorResourcePodSelected(pod, podNamespace)
			},
			maxSamples: opts.resourceMaxSamples, maxContainers: opts.resourceMaxContainers,
			maxThermal: opts.resourceMaxThermal, maxErrors: opts.resourceMaxErrors,
		}).receive,
	}
	// HTTP-boundary calls (reviews, settings ConfigMap) take their own client
	// budget so ingress and control calls never queue behind each other.
	reviews, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	scheduler, err := newSchedulerService(
		ctx, cluster, store, kube, reviews, execution, observer, objects, observationLog, opts,
		httpapi.MaximumInferenceRequestTimeout, publisher,
	)
	if err != nil {
		return fmt.Errorf("initialize scheduler service: %w", err)
	}
	defer scheduler.Close()
	authorizer := &servingkube.Authorizer{Client: reviews}
	measurements, err := newMeasurementService(store.Client(), kube, scheduler, objects, opts)
	if err != nil {
		return err
	}

	handlerConfig.ReadCatalog = scheduler.ReadCatalog
	handlerConfig.OccupancyObservations = scheduler.OccupancyObservations
	handlerConfig.SubmitMeasurement = measurements.Submit
	handlerConfig.DeleteMeasurement = measurements.Delete
	handlerConfig.BuildOccupancy = measurements.BuildOccupancy
	handlerConfig.SubmitOccupancy = measurements.SubmitOccupancy
	handlerConfig.DeleteOccupancy = measurements.DeleteOccupancy
	handlerConfig.OnMutationAction = publisher.action

	experiments := &experimentService{
		reader: evaluationObjects, lister: evaluationObjects, store: evaluationStoreName, maxBytes: opts.maxRequestBytes,
	}
	handlerConfig.ListExperiments = experiments.list
	handlerConfig.GetExperiment = experiments.get
	evaluations := evaluation.New(
		store.Client(), store.Client(), opts.servingNamespace, evaluationStoreName, evaluationSettings(scheduler))
	httpapi.ConfigureEvaluations(&handlerConfig, evaluations)
	configureEvaluationTemplate(&handlerConfig, evaluations, evaluationStoreName, experiments)
	evaluationLoads := evaluation.NewLoadService(store.Client(), opts.servingNamespace, evaluationObjects)
	httpapi.ConfigureLoadScenarios(&handlerConfig, evaluationLoads)
	handler, err := httpapi.NewHandler(manager, execution, authorizer, observationLog, observer, handlerConfig)
	if err != nil {
		return err
	}
	chillServer, err := composeChillServer(handler, authorizer, opts, measurements)
	if err != nil {
		return err
	}
	warming := &initializingHandler{initial: chillServer, ready: make(chan struct{}), live: publisher.hub}
	initialize := func(initializeCtx context.Context) (http.Handler, error) {
		if err := initializeServing(initializeCtx, &opts, observer, manager, execution,
			publisher, scheduler, &handlerConfig); err != nil {
			return nil, err
		}
		handler, err := httpapi.NewHandler(manager, execution, authorizer, observationLog, observer, handlerConfig)
		if err != nil {
			return nil, err
		}
		return composeChillServer(handler, authorizer, opts, measurements)
	}
	return serve(ctx, opts, warming, execution, observer, manager, observationLog, scheduler, diagnostics, initialize)
}

// closeObservationOwner releases the directory only after a completed local
// recording close; a failed close must not hand its unwritten tail to a successor.
func closeObservationOwner(owner *os.File, release bool) error {
	if !release {
		return nil
	}
	return owner.Close()
}

// Health and the dashboard remain reachable while state-dependent APIs warm.
// Closing ready publishes initialized to requests without a mutable HandlerConfig.
type initializingHandler struct {
	initial, initialized http.Handler
	ready                chan struct{}
	live                 *live.Hub
}

func (h *initializingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case <-h.ready:
		h.initialized.ServeHTTP(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/v1") {
			w.Header().Set("Retry-After", "2")
			httpapi.SendQueryError(w, &serving.Error{Code: "CoverageWarming", Message: "serving initialization is not complete"})
			return
		}
		h.initial.ServeHTTP(w, r)
	}
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
	observer *servingkube.Observer, manager *management.Manager, execution *serving.Runtime,
	publisher *livePublisher, scheduler *application.SchedulerService,
	handlerConfig *httpapi.HandlerConfig,
) error {
	if scheduler != nil {
		refreshCtx, cancel := context.WithTimeout(ctx, opts.operationTimeout)
		err := scheduler.Refresh(refreshCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("initialize scheduler service: %w", err)
		}
	}
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
	if scheduler != nil && len(scheduler.Controller.Settings().AutonomousWorkloads) > 0 {
		execution.EnableSchedulerRouting()
	}
	if scheduler != nil {
		handlerConfig.Scheduler = scheduler.Controller
		handlerConfig.SchedulerConfigMap = opts.schedulerConfigMap
		handlerConfig.SchedulerConfigKey = opts.schedulerConfigKey
		handlerConfig.SchedulerNamespace = opts.servingNamespace
		handlerConfig.ChangeSchedulerSettings = scheduler.ChangeSchedulerSettings
		handlerConfig.SchedulerMeters = scheduler.SchedulerMeters
		handlerConfig.OperatingPower = scheduler.OperatingPower
		handlerConfig.OperatingPowerSeries = scheduler.OperatingPowerSeries

	}
	return nil
}

func serve(ctx context.Context, opts options, chillServer *initializingHandler, execution *serving.Runtime,
	observer *servingkube.Observer, manager *management.Manager,
	observationLog *observation.ObservationLog, scheduler *application.SchedulerService, diagnostics *diagnosticSession,
	initialize func(context.Context) (http.Handler, error),
) error {
	ctx, cancelArchive := context.WithCancel(ctx)
	defer cancelArchive()
	certificate, err := tls.LoadX509KeyPair(opts.tlsCert, opts.tlsKey)
	if err != nil {
		return err
	}
	handlers := &joiningHandler{next: chillServer}
	server := &http.Server{
		Addr: opts.bindAddress, Handler: handlers,
		ReadHeaderTimeout: opts.requestTimeout, IdleTimeout: opts.requestTimeout,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
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
	go func() { serverErrors <- server.ServeTLS(listener, "", "") }()
	diagnostics.listenerBound()
	initializeCtx, cancelInitialize := context.WithCancel(ctx)
	initialized := make(chan error, 1)
	go func() {
		handler, err := initialize(initializeCtx)
		if err == nil {
			chillServer.initialized = handler
			close(chillServer.ready) // Publish only the complete handler configuration.
		}
		initialized <- err
	}()
	ready := false
	initializePending := true
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
		case err := <-initialized:
			initializePending = false
			if err != nil {
				serveError = err
				break loop
			}
			ready = true
			scheduler.Start()
		case <-ctx.Done():
			break loop
		case <-sourceTicker.C:
			if !ready {
				continue
			}
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
			if !ready {
				continue
			}
			callCtx, cancel := context.WithTimeout(ctx, opts.requestTimeout)
			if scheduler != nil {
				if err := scheduler.Refresh(callCtx); err != nil {
					ctrl.Log.Error(err, "refresh selected profiling evidence")
				}
			}
			cancel()
		case <-archiveTicker.C:
			archiveInBackground(ctx, archiving, observationLog.Flush)
		}
	}
	// Reject new public work before joining control and archive producers.
	handlers.stop()
	execution.BeginShutdown()
	cancelArchive() // Every exit, including listener/initialization errors, stops Flush.
	cancelInitialize()
	if initializePending {
		serveError = errors.Join(serveError, <-initialized)
	}
	archiving <- struct{}{} // The running Flush ends before Close archives.
	scheduler.Close()
	// Native watches remain alive through the accepted-work drain or its error boundary.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), opts.drainTimeout)
	defer cancelShutdown()
	shutdownError := shutdown(shutdownCtx, server, execution)
	// HTTP Shutdown does not close hijacked live sockets. Release their owner
	// before joining handlers, including listener-error exits with a live parent.
	if chillServer.live != nil {
		chillServer.live.Close()
	}
	handlers.wait()
	execution.JoinExecutions()
	if errors.Is(serveError, http.ErrServerClosed) {
		serveError = nil
	}
	return errors.Join(serveError, shutdownError)
}

// archiveInBackground starts one Flush unless one still runs, so source
// refresh and Advance keep their ticks while an hour uploads.
func archiveInBackground(
	ctx context.Context, running chan struct{}, flush func(context.Context) error,
) {
	select {
	case running <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-running }()
		if err := flush(ctx); err != nil {
			ctrl.Log.Error(err, "archive serving observation hours")
		}
	}()
}

// joiningHandler owns the public producer boundary, including handlers still
// unwinding after Server.Close cancels their connections on a failed drain.
type joiningHandler struct {
	next    http.Handler
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
}

func (h *joiningHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()
	h.next.ServeHTTP(w, r)
}

func (h *joiningHandler) stop() {
	h.mu.Lock()
	h.stopped = true
	h.mu.Unlock()
}

func (h *joiningHandler) wait() { h.wg.Wait() }

func shutdown(ctx context.Context, server *http.Server, execution *serving.Runtime) error {
	drainErrors := make(chan error, 1)
	go func() { drainErrors <- execution.Shutdown(ctx) }()
	shutdownError := server.Shutdown(ctx)
	if shutdownError != nil {
		shutdownError = errors.Join(shutdownError, server.Close())
	}
	return errors.Join(shutdownError, <-drainErrors)
}
