package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime/debug"
	"syscall"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilerapp "github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/application"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newSchedulerService(
	ctx context.Context, cluster *servingkube.ClusterState, store *servingkube.Store,
	kube, settingsKube kubernetes.Interface,
	execution *serving.Runtime, observer *servingkube.Observer, objects *stores3.Store,
	observationLog *observation.ObservationLog, opts options,
	gatewayRequestTimeout time.Duration, publisher *livePublisher,
) (*application.SchedulerService, error) {
	if opts.schedulerConfigMap == "" {
		return nil, nil
	}
	operations, err := servingops.NewOperations(
		cluster, store, kube, execution, observer, &sdk.Client{}, servingops.OperationConfig{
			Namespace:    opts.servingNamespace,
			PollInterval: opts.sourceInterval,
			MetricLimits: metricobserver.Limits{
				MaxResultBytes: opts.maxEvidenceBytes, MemoryBudgetBytes: debug.SetMemoryLimit(-1),
			},
		})
	if err != nil {
		return nil, err
	}
	catalog, err := servingkube.NewCatalog(store, objects, opts.maxEvidenceBytes)
	var costs *servingkube.CostEvidence
	if err == nil {
		costs, err = servingkube.NewCostEvidence(store, objects, opts.maxEvidenceBytes)
	}
	session := servingkube.SessionConfig{
		Image: opts.sessionLoadImage, Namespace: opts.loadGenerator.Namespace,
		ServiceAccount: opts.loadGenerator.ServiceAccount, NodeName: opts.loadGenerator.NodeName,
		MaxResultBytes: opts.maxEvidenceBytes,
		// The serving preparation reads the same two options.
		RunnerTimeout: opts.operationTimeout, MaxRequestBytes: opts.maxRequestBytes,
	}
	if err == nil {
		session.Prepare, err = newSessionPrepare(store.Client(), opts)
	}
	if err != nil {
		return nil, err
	}
	var live *application.Live
	if publisher != nil {
		live = &application.Live{Decision: publisher.decision, Action: publisher.action, PowerScope: publisher.powerScope,
			MeterSource: func(source metricobserver.Source, nodeUID string) metricobserver.Source {
				return livePowerSource{Source: source, metrics: publisher.metrics, nodeUID: nodeUID}
			}}
	}
	return application.NewSchedulerService(ctx, application.Dependencies{
		Cluster:    cluster,
		Operations: operations, Store: store, Kube: kube, SettingsKube: settingsKube,
		Execution: execution, Observer: observer, Objects: objects,
		ObservationLog: observationLog, GatewayRequestTimeout: gatewayRequestTimeout, Live: live,
		Catalog: catalog, Costs: costs,
		Options: application.Options{
			ActuationPoliciesKey:   opts.actuationPoliciesKey,
			FactsConfigMap:         opts.factsConfigMap,
			FactsNamespace:         opts.factsNamespace,
			LoadGenerator:          opts.loadGenerator,
			MaxEvidenceBytes:       opts.maxEvidenceBytes,
			MaxRequestBytes:        opts.maxRequestBytes,
			MetricSourcesKey:       opts.metricSourcesKey,
			OperationTimeout:       opts.operationTimeout,
			ProfileNamespaces:      opts.profileNamespaces,
			RequestTimeout:         opts.requestTimeout,
			SchedulerConfigKey:     opts.schedulerConfigKey,
			SchedulerConfigMap:     opts.schedulerConfigMap,
			ServingNamespace:       opts.servingNamespace,
			Session:                session,
			StoreBucket:            opts.storeBucket,
			StoreCredentialsSecret: opts.storeCredentialsSecret,
			StoreEndpoint:          opts.storeEndpoint,
			StoreName:              opts.storeName,
			StoreRegion:            opts.storeRegion,
		},
	}), nil
}

// newSessionPrepare resolves a measurement request's session from the Profiler
// facts through the resolvers chill-query's preflight uses. Without Profiler
// facts it is nil and no session runs.
func newSessionPrepare(
	reader client.Reader, opts options,
) (func(context.Context, *edge.BatchEnergyOptimization) (servingkube.SessionPreparation, error), error) {
	if opts.factsConfigMap == "" {
		return nil, nil
	}
	namespace, name := opts.factsNamespace, opts.factsConfigMap
	candidates, candidateErr := profilerkube.NewKubernetesCandidateResolver(reader, profilerkube.CandidateResolverOptions{
		Namespace: namespace, ConfigMapName: name, ConfigMapKey: opts.executionFactsKey,
	})
	nodes, nodeErr := profilerkube.NewKubernetesNodeResolver(reader)
	metrics, metricErr := profilerkube.NewKubernetesMetricSourceResolver(reader, profilerkube.MetricSourceResolverOptions{
		Namespace: namespace, ConfigMapName: name, ConfigMapKey: opts.metricSourcesKey,
	})
	workloads, workloadErr := profilerkube.NewKubernetesWorkloadObservationResolver(reader,
		profilerkube.WorkloadObservationResolverOptions{
			Namespace: namespace, ConfigMapName: name, ConfigMapKey: opts.workloadObservationKey,
		})
	actuation, actuationErr := profilerkube.NewKubernetesActuationPolicyResolver(reader,
		profilerkube.ActuationPolicyResolverOptions{
			Namespace: namespace, ConfigMapName: name, ConfigMapKey: opts.actuationPoliciesKey,
		})
	execution, executionErr := profilerkube.NewKubernetesExecutionPolicyResolver(reader,
		profilerkube.ExecutionPolicyResolverOptions{
			Namespace: namespace, ConfigMapName: name, ConfigMapKey: opts.executionPoliciesKey,
			MeasurementMemoryFraction: opts.measurementMemoryFraction,
		})
	if err := errors.Join(candidateErr, nodeErr, metricErr, workloadErr, actuationErr, executionErr); err != nil {
		return nil, fmt.Errorf("construct session resolvers: %w", err)
	}
	preparer, err := profilerapp.NewPreparer(candidates, nodes, metrics, workloads, actuation, execution,
		func(ctx context.Context, node string) (string, profilingrun.LoadOriginGatewayPodContract, error) {
			return profilerkube.ResolveGatewayLoadOrigin(ctx, reader, namespace, opts.resourceSystem, node)
		})
	if err != nil {
		return nil, fmt.Errorf("construct session preparer: %w", err)
	}
	return func(ctx context.Context, beo *edge.BatchEnergyOptimization) (servingkube.SessionPreparation, error) {
		state, protocol := beo.Spec.State, beo.Spec.ActiveProtocol
		session, err := preparer.PrepareSession(ctx, profilerapp.SessionRequest{
			Candidate: profilerapp.CandidateSelection{
				ModelSpec: state.Candidate.ModelSpec, DeviceClass: state.Candidate.DeviceClass, Node: state.TargetNode,
				ExecutionPath: state.Candidate.ExecutionPath, PowerMode: state.Candidate.PowerMode,
				Preprocessor: state.Candidate.Preprocessor, Dataset: protocol.Dataset,
			},
			Workload: protocol.Workload, WarmupIterations: int(protocol.WarmupIterations),
		})
		if err != nil {
			return servingkube.SessionPreparation{}, sessionResolutionError(err)
		}
		meter, err := metrics.ResolveMetricSource(ctx, state.TargetNode)
		if err != nil {
			return servingkube.SessionPreparation{}, sessionResolutionError(fmt.Errorf("resolve Node meter: %w", err))
		}
		config := session.RunConfig
		delivery, err := json.Marshal(config.Delivery)
		if err != nil {
			return servingkube.SessionPreparation{}, err
		}
		// PrepareSession freezes the requested dataset or fails.
		return servingkube.SessionPreparation{
			RunConfig: config, MemoryBytes: uint64(config.Actor.MemoryLimitBytes), RuntimeDelivery: delivery,
			BatchSizes: session.PlannedBatchSizes, SLOPercentile: config.Measurement.SLOPercentile,
			SLOTarget: time.Duration(config.Measurement.SLOTargetLatencyNanos), Dataset: *config.Measurement.Dataset,
			// The same mapping declares ChillServer's meters from these facts.
			Meter: metricobserver.SourceIdentity{
				Kind: meter.Kind, NodeName: state.TargetNode, Endpoint: meter.Endpoint, Metric: meter.PowerMetric,
				MetricLabels: meter.MetricLabels,
			},
		}, nil
	}, nil
}

func sessionResolutionError(err error) error {
	var network net.Error
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) ||
		apierrors.IsServiceUnavailable(err) || apierrors.IsInternalError(err) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return &servingkube.SessionResolutionRetry{Err: err}
	}
	return err
}
