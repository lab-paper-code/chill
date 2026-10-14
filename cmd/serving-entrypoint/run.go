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
	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
	"github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	journal, err := serving.NewJournal(serving.JournalConfig{
		Directory: opts.observationDirectory, Store: opts.storeName, Window: opts.observationWindow,
		Writer: objects, Definitions: opts.definitions, Outcomes: opts.outcomes,
	})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), opts.requestTimeout)
		defer cancel()
		if err := journal.Close(closeCtx); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close serving observations: %w", err))
		}
	}()
	// Backend lifetime belongs to the owner, not SIGTERM or an HTTP caller.
	backendCtx, cancelBackend := context.WithCancel(context.Background())
	defer cancelBackend()
	execution, err := serving.NewRuntime(backendCtx, serving.RuntimeConfig{QueueLimit: opts.queueLimit, Journal: journal})
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
	catalog, err := servingkube.NewCatalog(store, objects, opts.maxEvidenceBytes, opts.profileConfidenceLevel)
	if err != nil {
		return err
	}
	manager, err := servingkube.NewManager(store, execution, journal, observer,
		servingkube.ManagerConfig{DrainTimeout: opts.drainTimeout})
	if err != nil {
		return err
	}
	initializeCtx, cancelInitialize := context.WithTimeout(ctx, opts.requestTimeout)
	err = refreshProfiles(initializeCtx, catalog, observer, opts.campaigns)
	if err == nil {
		err = observer.Refresh(initializeCtx)
	}
	if err == nil {
		err = manager.Initialize(initializeCtx)
	}
	cancelInitialize()
	if err != nil {
		return fmt.Errorf("initialize Workload owner: %w", err)
	}
	scheduler, err := newSchedulerOwner(ctx, store, kube, execution, observer, objects, journal, opts)
	if err != nil {
		return fmt.Errorf("initialize scheduler owner: %w", err)
	}
	defer scheduler.Close()
	handlerConfig := entrypoint.HandlerConfig{
		MaxRequestBytes: opts.maxRequestBytes, RequestTimeout: opts.requestTimeout,
		MaxQueryEvents: opts.maxQueryEvents, ObservationWindow: opts.observationWindow,
	}
	if scheduler != nil {
		handlerConfig.Scheduler = scheduler.controller
		handlerConfig.SchedulerConfigMap = opts.schedulerConfigMap
		handlerConfig.SchedulerConfigKey = opts.schedulerConfigKey
		handlerConfig.SchedulerNamespace = opts.servingNamespace
		handlerConfig.SchedulerLock = &scheduler.lock
		handlerConfig.OperatingPower = scheduler.operatingPower
	}
	authorizer := &servingkube.Authorizer{Client: kube}
	handler, err := entrypoint.NewHandler(manager, execution, authorizer, journal, observer, handlerConfig)
	if err != nil {
		return err
	}
	owner, err := composeOwner(handler, authorizer, opts)
	if err != nil {
		return err
	}
	return serve(ctx, opts, owner, execution, observer, catalog, manager, journal, scheduler)
}

func serve(ctx context.Context, opts options, owner http.Handler, execution *serving.Runtime,
	observer *servingkube.Observer, catalog *servingkube.Catalog, manager *servingkube.Manager,
	journal *serving.Journal, scheduler *schedulerOwner,
) error {
	server := &http.Server{
		Addr: opts.bindAddress, Handler: owner,
		ReadHeaderTimeout: opts.requestTimeout, IdleTimeout: opts.requestTimeout,
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServeTLS(opts.tlsCert, opts.tlsKey) }()
	scheduler.Start()
	sourceTicker := time.NewTicker(opts.sourceInterval)
	defer sourceTicker.Stop()
	profileTicker := time.NewTicker(opts.profileInterval)
	defer profileTicker.Stop()
	archiveTicker := time.NewTicker(opts.observationWindow)
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
			if err := refreshProfiles(callCtx, catalog, observer, opts.campaigns); err != nil {
				ctrl.Log.Error(err, "refresh selected profiling evidence")
			}
			cancel()
		case <-archiveTicker.C:
			callCtx, cancel := context.WithTimeout(ctx, opts.requestTimeout)
			if err := journal.Flush(callCtx); err != nil {
				ctrl.Log.Error(err, "publish serving observation window")
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

func refreshProfiles(ctx context.Context, catalog *servingkube.Catalog,
	observer *servingkube.Observer, campaigns []types.NamespacedName,
) error {
	view, err := catalog.Refresh(ctx, campaigns)
	if err != nil {
		return err
	}
	observer.SetProfiles(view.Profiles)
	for _, issue := range view.Insufficiencies {
		ctrl.Log.Info("selected profile is insufficient",
			"campaign", issue.Source.String(), "code", issue.Code, "reason", issue.Reason)
	}
	return nil
}
