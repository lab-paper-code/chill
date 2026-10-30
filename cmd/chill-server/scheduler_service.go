package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

type powerObservationConfig struct {
	Sampling metricobserver.Request `json:"sampling"`
	Coverage powerwindow.Policy     `json:"coverage"`
}

type schedulerConfiguration struct {
	Settings serving.SchedulerSettings              `json:"settings"`
	Reserved map[string]serving.SchedulerAllocation `json:"reserved"`
	// Meters are derived from the Profiler metric sources, never configured.
	Meters           []servingkube.MeterDeclaration `json:"-"`
	PowerObservation *powerObservationConfig        `json:"powerObservation,omitempty"`
	StaticRouting    map[string]map[string]int      `json:"staticRouting,omitempty"`
	// StaticRoutingValidatedAt names the settings change that wrote StaticRouting.
	StaticRoutingValidatedAt *time.Time `json:"staticRoutingValidatedAt,omitempty"`
}

type schedulerService struct {
	lock                     sync.Mutex
	controller               *servingkube.SchedulerController
	operations               *servingkube.Operations
	store                    *servingkube.Store
	kube                     kubernetes.Interface
	execution                *serving.Runtime
	observer                 *servingkube.Observer
	objects                  *stores3.Store
	observationLog           *serving.ObservationLog
	live                     *livePublisher
	opts                     options
	gatewayRequestTimeout    time.Duration
	catalog                  *servingkube.Catalog
	costs                    *servingkube.CostEvidence
	resourceVersion, sources string
	configuration            atomic.Pointer[schedulerConfiguration]
	profiles                 atomic.Pointer[httpapi.ProfilesView]
	ctx                      context.Context
	cancel                   context.CancelFunc
	powerCancel              context.CancelFunc
	wg                       sync.WaitGroup
	powerWG                  sync.WaitGroup
}

func newSchedulerService(ctx context.Context, store *servingkube.Store, kube kubernetes.Interface,
	execution *serving.Runtime, observer *servingkube.Observer, objects *stores3.Store,
	observationLog *serving.ObservationLog, opts options, gatewayRequestTimeout time.Duration, publisher *livePublisher,
) (*schedulerService, error) {
	if opts.schedulerConfigMap == "" {
		return nil, nil
	}
	operations, err := servingkube.NewOperations(store, kube, execution, observer, &sdk.Client{},
		servingkube.OperationConfig{
			Namespace:    opts.servingNamespace,
			PollInterval: opts.sourceInterval,
		})
	if err != nil {
		return nil, err
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	service := &schedulerService{
		operations: operations, store: store, kube: kube, execution: execution, observer: observer,
		objects: objects, observationLog: observationLog, opts: opts, ctx: serviceCtx, cancel: cancel,
		gatewayRequestTimeout: gatewayRequestTimeout,
		live:                  publisher,
	}
	service.catalog, err = servingkube.NewCatalog(store, objects, opts.maxEvidenceBytes, opts.profileConfidenceLevel)
	if err == nil {
		service.costs, err = servingkube.NewCostEvidence(store, objects, opts.maxEvidenceBytes)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	initializeCtx, cancelInitialize := context.WithTimeout(ctx, opts.requestTimeout)
	defer cancelInitialize()
	if err = service.refresh(initializeCtx); err != nil {
		service.Close()
		return nil, err
	}
	return service, nil
}

func decodeSchedulerConfiguration(raw string, maxBytes int64) (schedulerConfiguration, error) {
	// The wire settings field shadows the embedded one, so an omitted margin stays distinguishable from zero.
	var wire struct {
		schedulerConfiguration
		Settings serving.SchedulerSettingsInput `json:"settings"`
	}
	if int64(len(raw)) > maxBytes {
		return schedulerConfiguration{}, errors.New("scheduler configuration exceeds body bound")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return schedulerConfiguration{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return schedulerConfiguration{}, errors.New("one scheduler configuration object required")
	}
	config := wire.schedulerConfiguration
	var err error
	if config.Settings, err = wire.Settings.Settings(); err != nil {
		return config, err
	}
	if err := config.validateInventory(); err != nil {
		return config, err
	}
	if err := config.validatePower(); err != nil {
		return config, err
	}
	return config, nil
}

// validateInventory checks the operator reservation; the live Node inventory
// it must fall within is checked at capture.
func (config schedulerConfiguration) validateInventory() error {
	for node, reservation := range config.Reserved {
		seen := map[int]bool{}
		for _, cpu := range reservation.CPUs {
			if cpu < 0 || seen[cpu] {
				return fmt.Errorf("Node %s reserved CPUs must be unique nonnegative physical IDs", node)
			}
			seen[cpu] = true
		}
	}
	return nil
}

func (config schedulerConfiguration) validatePower() error {
	if config.PowerObservation != nil {
		if err := config.PowerObservation.Sampling.Validate(); err != nil {
			return err
		}
		p := config.PowerObservation.Coverage
		if p.MaximumBoundaryDistance <= 0 || p.MaximumBoundaryDistance > time.Minute || p.MaximumGap <= 0 ||
			math.IsNaN(p.MinimumCoverage) || p.MinimumCoverage <= 0 || p.MinimumCoverage > 1 {
			return errors.New("explicit finite power coverage policy required")
		}
	}
	return nil
}

// meterClient keeps one idle connection per concurrently polled meter, so
// meters behind one power server (shelly_server) reuse their connections.
var meterClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 64
	return &http.Client{Transport: transport}
}()

func metricSource(identity metricobserver.SourceIdentity) (metricobserver.Source, error) {
	if identity.ThermalMetrics != nil {
		return edgemetrics.New(identity, meterClient)
	}
	return edgemetrics.NewPowerSeries(identity, meterClient)
}

func (o *schedulerService) refresh(ctx context.Context) (err error) {
	o.lock.Lock()
	defer o.lock.Unlock()
	return o.refreshLocked(ctx)
}

// refreshLocked publishes the complete accepted configuration while the service
// excludes Tick and operator mutations.
func (o *schedulerService) refreshLocked(ctx context.Context) (err error) {
	cm, err := o.kube.CoreV1().ConfigMaps(o.opts.servingNamespace).Get(ctx, o.opts.schedulerConfigMap, meta.GetOptions{})
	if err != nil {
		return err
	}
	config, err := decodeSchedulerConfiguration(cm.Data[o.opts.schedulerConfigKey], o.opts.maxRequestBytes)
	if err != nil {
		return err
	}
	policies, factsVersion, err := o.profilerFacts(ctx, &config)
	if err != nil {
		return err
	}
	namespaces := o.opts.profileNamespaces
	if len(namespaces) == 0 {
		namespaces = []string{o.opts.servingNamespace}
	}
	view, err := o.catalog.Refresh(ctx, namespaces)
	if err != nil {
		return err
	}
	costs, err := o.costs.Refresh(ctx, namespaces, config.Meters, view.Profiles)
	if err != nil {
		return err
	}
	sources := costs.Sources
	sources = append(sources, "profiler-facts:"+factsVersion)
	for _, meter := range config.Meters {
		sources = append(sources, "meter:"+meter.ID+"/"+meter.Node.UID)
	}
	for _, p := range view.Profiles {
		sources = append(sources, p.ID)
	}
	for _, issue := range costs.Issues {
		sources = append(sources, issue.Code+":"+issue.Source)
	}
	// A catalog issue refuses the measurements naming its profile.
	for _, issue := range view.Insufficiencies {
		sources = append(sources, "catalog:"+issue.Code+":"+issue.Source.String())
	}
	if len(view.ReuseChecks) != 0 {
		checks, err := json.Marshal(view.ReuseChecks)
		if err != nil {
			return err
		}
		sources = append(sources, "profile-reuse:"+string(checks))
	}
	slices.Sort(sources)
	signature := strings.Join(sources, "\x00")
	if o.controller != nil && cm.ResourceVersion == o.resourceVersion && signature == o.sources {
		return nil
	}
	if err = o.retainProfileReuseChecks(ctx, &view); err != nil {
		return err
	}
	preparations, issues := o.preparations(ctx, config, view.Profiles)
	view.Insufficiencies = append(view.Insufficiencies, issues...)
	controllerConfig := servingkube.SchedulerControllerConfig{
		Settings:          config.Settings,
		ConfigurationName: o.opts.schedulerConfigMap, ConfigurationKey: o.opts.schedulerConfigKey,
		Reserved: config.Reserved, Preparations: preparations, Costs: costs.Costs, Baselines: costs.Baselines,
		CostIssues: costs.Issues, DispatchOccupancy: costs.DispatchOccupancy, CatalogIssues: view.Insufficiencies,
		LoadGenerator:         o.opts.loadGenerator,
		GatewayRequestTimeout: o.gatewayRequestTimeout,
		Meters:                config.Meters, PowerPolicies: policies,
		StaticRouting: len(config.StaticRouting) > 0, MeasurementNamespaces: namespaces,
		Evidence: o.objects, EvidenceStore: o.opts.storeName, MeterSource: metricSource,
		OnDecision: o.recordDecision, OnAction: o.recordAction,
	}
	if o.controller == nil {
		o.controller, err = servingkube.NewSchedulerController(o.store, o.kube, o.execution,
			o.observer, o.operations, o.observationLog, controllerConfig)
	} else {
		err = o.controller.SetConfiguration(controllerConfig)
	}
	if err != nil {
		return err
	}
	// Publish the complete configuration only after the controller accepted it.
	o.observer.SetProfiles(view.Profiles)
	o.execution.SetCompletionMargin(config.Settings.CompletionMarginMS)
	o.configuration.Store(&config)
	published := httpapi.ProfilesView{
		ConfigVersion: cm.ResourceVersion, CatalogView: view,
		Preparations: make(map[string]servingkube.PrepareSpec, len(preparations)),
	}
	for id, spec := range preparations {
		spec.Instance.Profile = nil // the profile body is in Profiles
		published.Preparations[id] = spec
	}
	o.profiles.Store(&published)
	o.resourceVersion = cm.ResourceVersion
	o.sources = signature
	o.restartPower(config)
	return nil
}

// profilerFacts reads the configured actuation policies and derives the wall
// meters from the Profiler facts. Without the Profiler there are none: no Node
// takes power requests and no meter is declared. A meter binds to the live
// Node of its name; a source whose Node is absent declares no meter.
func (o *schedulerService) profilerFacts(
	ctx context.Context, config *schedulerConfiguration,
) ([]nodeactuator.ActuationPolicySpec, string, error) {
	if o.opts.factsConfigMap == "" {
		return nil, "", nil
	}
	cm, err := o.kube.CoreV1().ConfigMaps(o.opts.factsNamespace).Get(ctx, o.opts.factsConfigMap, meta.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	policies, err := profiler.DecodeActuationPolicyCatalog([]byte(cm.Data[o.opts.actuationPoliciesKey]))
	if err != nil {
		return nil, "", err
	}
	sources, err := profilerkube.DecodeMetricSources([]byte(cm.Data[o.opts.metricSourcesKey]))
	if err != nil {
		return nil, "", err
	}
	nodes, err := o.kube.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		return nil, "", err
	}
	uids := make(map[string]string, len(nodes.Items))
	for _, n := range nodes.Items {
		uids[n.Name] = string(n.UID)
	}
	for _, s := range sources {
		if uid := uids[s.Node]; uid != "" {
			config.Meters = append(config.Meters, servingkube.MeterDeclaration{
				ID: s.MeterID, Node: serving.NodeReference{Name: s.Node, UID: uid}, Scope: "node-exclusive-wall",
				Source: metricobserver.SourceIdentity{
					Kind: s.Kind, NodeName: s.Node, Endpoint: s.Endpoint, Metric: s.PowerMetric, MetricLabels: s.MetricLabels,
				},
			})
		}
	}
	return policies, cm.ResourceVersion, nil
}

func (o *schedulerService) operatingPower(
	ctx context.Context, from, to time.Time, limit int,
) (serving.OperatingPowerWindow, error) {
	// Capture one accepted configuration without waiting behind refresh or Tick.
	config := o.configuration.Load()
	meters := make([]serving.OperatingPowerMeter, len(config.Meters))
	for index, meter := range config.Meters {
		meters[index] = serving.OperatingPowerMeter{
			Node: meter.Node, Meter: meter.ID, Scope: meter.Scope, Source: meter.Source,
		}
	}
	return o.observationLog.OperatingPower(ctx, from, to, meters, limit)
}

func (o *schedulerService) recordDecision(
	decision serving.SchedulerDecision, snapshot serving.SchedulerSnapshot,
) error {
	if err := o.observationLog.SchedulerRecord(serving.SchedulerObservation{
		At: decision.At, Decision: &decision, Snapshot: &snapshot,
	}); err != nil {
		return err
	}
	if o.live != nil {
		o.live.decision(decision, snapshot)
	}
	return nil
}
func (o *schedulerService) recordAction(result servingkube.OperationResult) error {
	expected, err := json.Marshal(result.Operation)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(result)
	if err != nil {
		return err
	}
	event := serving.SchedulerActionObservation{
		ID: result.Operation.ID, Kind: result.Operation.Kind, Node: result.Operation.Node,
		Instance: result.Operation.Instance, Phase: result.Status, StartedAt: result.StartedAt,
		Expected: expected, Actual: actual, Error: result.Error,
		ExpectedSeconds: result.Operation.ExpectedSeconds, ExpectedSource: result.Operation.ExpectedSource,
	}
	if !result.EndedAt.IsZero() {
		event.CompletedAt = &result.EndedAt
	}
	if err := o.observationLog.SchedulerRecord(serving.SchedulerObservation{
		At: time.Now().UTC(), Action: &event,
	}); err != nil {
		return err
	}
	if o.live != nil {
		o.live.action(result)
	}
	return nil
}

func (o *schedulerService) restartPower(config schedulerConfiguration) {
	if o.powerCancel != nil {
		o.powerCancel()
		o.powerWG.Wait()
	}
	if o.live != nil {
		o.live.powerScope(config.Meters, config.PowerObservation != nil)
	}
	if config.PowerObservation == nil {
		return
	}
	ctx, cancel := context.WithCancel(o.ctx)
	o.powerCancel = cancel
	for _, meter := range config.Meters {
		o.powerWG.Add(1)
		go func(meter servingkube.MeterDeclaration) {
			defer o.powerWG.Done()
			source, err := metricSource(meter.Source)
			if err != nil {
				ctrl.Log.Error(err, "configure scheduler wall meter")
				return
			}
			if o.live != nil {
				source = livePowerSource{Source: source, metrics: o.live.metrics, nodeUID: meter.Node.UID}
			}
			observer, err := metricobserver.New(source)
			if err != nil {
				ctrl.Log.Error(err, "configure power observer")
				return
			}
			sampling := config.PowerObservation.Sampling
			coverage := config.PowerObservation.Coverage
			for ctx.Err() == nil {
				result, observeErr := observer.Observe(ctx, sampling, func() {})
				samples := make([]powerwindow.Sample, len(result.Samples))
				for i, sample := range result.Samples {
					samples[i] = powerwindow.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
				}
				joules, coverageErr := powerwindow.Integrate(powerwindow.Window{
					Start: result.StartedAt, End: result.EndedAt, Samples: samples,
				}, coverage)
				event := serving.SchedulerObservation{
					At: result.EndedAt, Power: &result, Node: &meter.Node, Meter: meter.ID,
					PowerScope: meter.Scope, PowerSampling: &sampling, PowerCoverage: &coverage,
				}
				if err = errors.Join(observeErr, coverageErr); err != nil {
					event.PowerError = err.Error()
				} else {
					event.PowerJoules = &joules
				}
				if err = o.observationLog.SchedulerRecord(event); err != nil {
					ctrl.Log.Error(err, "persist scheduler whole-node power observation")
					return
				}
			}
		}(meter)
	}
}

func (o *schedulerService) Start() {
	if o == nil {
		return
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		for o.ctx.Err() == nil {
			callCtx, cancel := context.WithTimeout(o.ctx, o.opts.requestTimeout)
			started := time.Now()
			if err := o.refresh(callCtx); err != nil {
				ctrl.Log.Error(err, "refresh scheduler operator configuration")
			} else {
				o.lock.Lock()
				if err := o.controller.Admit(callCtx); err != nil {
					ctrl.Log.Error(err, "admit measurements")
				}
				_, err := o.controller.Tick(callCtx, time.Now().UTC())
				o.lock.Unlock()
				if err != nil {
					ctrl.Log.Error(err, "evaluate and record scheduler control interval")
				}
			}
			cancel()
			timer := time.NewTimer(max(time.Until(started.Add(o.controller.Settings().ControlInterval)), 0))
			select {
			case <-o.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
func (o *schedulerService) Close() {
	if o == nil {
		return
	}
	o.cancel()
	o.wg.Wait()
	if o.powerCancel != nil {
		o.powerCancel()
	}
	o.powerWG.Wait()
	if o.controller != nil {
		o.controller.Close()
	}
}
