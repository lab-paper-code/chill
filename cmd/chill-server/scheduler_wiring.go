package main

import (
	"context"
	"runtime/debug"
	"time"

	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/application"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/client-go/kubernetes"
)

func newSchedulerService(ctx context.Context, store *servingkube.Store, kube kubernetes.Interface,
	execution *serving.Runtime, observer *servingkube.Observer, objects *stores3.Store,
	observationLog *serving.ObservationLog, opts options, gatewayRequestTimeout time.Duration, publisher *livePublisher,
) (*application.SchedulerService, error) {
	if opts.schedulerConfigMap == "" {
		return nil, nil
	}
	operations, err := servingkube.NewOperations(store, kube, execution, observer, &sdk.Client{},
		servingkube.OperationConfig{
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
		Operations: operations, Store: store, Kube: kube, Execution: execution, Observer: observer, Objects: objects,
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
			SourceInterval:         opts.sourceInterval,
			StoreBucket:            opts.storeBucket,
			StoreCredentialsSecret: opts.storeCredentialsSecret,
			StoreEndpoint:          opts.storeEndpoint,
			StoreName:              opts.storeName,
			StoreRegion:            opts.storeRegion,
		},
	}), nil
}
