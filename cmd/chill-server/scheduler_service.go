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

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
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
	Settings         serving.SchedulerSettings              `json:"settings"`
	CPUInventory     map[string][]int                       `json:"cpuInventory"`
	GPUInventory     map[string][]string                    `json:"gpuInventory"`
	Reserved         map[string]serving.SchedulerAllocation `json:"reserved"`
	Meters           []servingkube.MeterDeclaration         `json:"meters"`
	PowerRequests    map[string]*edge.NodeActuation         `json:"powerRequests"`
	PowerObservation *powerObservationConfig                `json:"powerObservation,omitempty"`
	StaticRouting    map[string]map[string]int              `json:"staticRouting,omitempty"`
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
	opts                     options
	catalog                  *servingkube.Catalog
	costs                    *servingkube.CostEvidence
	resourceVersion, sources string
	staticAppliedRV          string
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
	observationLog *serving.ObservationLog, opts options,
) (*schedulerService, error) {
	if opts.schedulerConfigMap == "" {
		return nil, nil
	}
	operations, err := servingkube.NewOperations(store, kube, execution, observer, &sdk.Client{},
		servingkube.OperationConfig{
			Namespace: opts.servingNamespace, ActuationNamespace: opts.actuationNamespace,
			PollInterval: opts.sourceInterval,
		})
	if err != nil {
		return nil, err
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	service := &schedulerService{
		operations: operations, store: store, kube: kube, execution: execution, observer: observer,
		objects: objects, observationLog: observationLog, opts: opts, ctx: serviceCtx, cancel: cancel,
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

func (config schedulerConfiguration) validateInventory() error {
	for node, cpus := range config.CPUInventory {
		seen := map[int]bool{}
		for _, cpu := range cpus {
			if cpu < 0 || seen[cpu] {
				return fmt.Errorf("Node %s CPU inventory must have unique nonnegative physical IDs", node)
			}
			seen[cpu] = true
		}
	}
	for node, gpus := range config.GPUInventory {
		seen := map[string]bool{}
		for _, gpu := range gpus {
			if gpu == "" || seen[gpu] {
				return fmt.Errorf("Node %s GPU inventory must have unique physical UUIDs", node)
			}
			seen[gpu] = true
		}
	}
	for node, reservation := range config.Reserved {
		seen := map[int]bool{}
		for _, cpu := range reservation.CPUs {
			if seen[cpu] || !slices.Contains(config.CPUInventory[node], cpu) {
				return fmt.Errorf("Node %s reserved CPUs must select its unique physical inventory", node)
			}
			seen[cpu] = true
		}
		if reservation.GPUUUID != "" && !slices.Contains(config.GPUInventory[node], reservation.GPUUUID) {
			return fmt.Errorf("Node %s reserved GPU is not in its inventory", node)
		}
	}
	return nil
}

func (config schedulerConfiguration) validatePower() error {
	meters := map[string]serving.NodeReference{}
	nodes := map[serving.NodeReference]string{}
	for _, meter := range config.Meters {
		if meter.ID == "" || meter.Scope != "node-exclusive-wall" ||
			meter.Node.Name == "" || meter.Node.UID == "" || meter.Source.NodeName != meter.Node.Name {
			return errors.New("exact physical Node exclusive wall-meter identity required")
		}
		if _, ok := meters[meter.ID]; ok {
			return errors.New("duplicate wall meter identity")
		}
		if _, ok := nodes[meter.Node]; ok {
			return errors.New("one declared wall meter per physical Node required")
		}
		meters[meter.ID] = meter.Node
		nodes[meter.Node] = meter.ID
		if _, err := metricSource(meter.Source); err != nil {
			return err
		}
	}
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

func metricSource(identity metricobserver.SourceIdentity) (metricobserver.Source, error) {
	if identity.ThermalMetrics != nil {
		return edgemetrics.New(identity, &http.Client{})
	}
	return edgemetrics.NewPowerSeries(identity, &http.Client{})
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
	for _, p := range view.Profiles {
		sources = append(sources, p.ID)
	}
	for _, issue := range costs.Issues {
		sources = append(sources, issue.Code+":"+issue.Source)
	}
	slices.Sort(sources)
	signature := strings.Join(sources, "\x00")
	if o.controller != nil && cm.ResourceVersion == o.resourceVersion && signature == o.sources {
		o.applyStaticRouting(ctx)
		return nil
	}
	preparations, issues := o.preparations(ctx, config, view.Profiles)
	view.Insufficiencies = append(view.Insufficiencies, issues...)
	controllerConfig := servingkube.SchedulerControllerConfig{
		Settings: config.Settings, CPUInventory: config.CPUInventory, GPUInventory: config.GPUInventory,
		Reserved: config.Reserved, Preparations: preparations, Costs: costs.Costs, Baselines: costs.Baselines,
		CostIssues: costs.Issues, Meters: config.Meters, PowerRequests: config.PowerRequests,
		StaticRouting: config.StaticRouting != nil,
		OnDecision:    o.recordDecision, OnAction: o.recordAction,
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
	o.applyStaticRouting(ctx)
	return nil
}

// An acknowledged configuration can await actual physical readiness. The normal
// refresh cadence applies its manual distribution for disabled Workloads once.
func (o *schedulerService) applyStaticRouting(ctx context.Context) {
	config := o.configuration.Load()
	if config.StaticRouting == nil {
		o.staticAppliedRV = ""
		return
	}
	if o.staticAppliedRV == o.resourceVersion {
		return
	}
	if err := o.controller.ApplyStaticRouting(ctx, config.StaticRouting, o.resourceVersion); err != nil {
		ctrl.Log.Error(err, "manual staticRouting remains unapplied", "sourceConfigRV", o.resourceVersion)
		return
	}
	o.staticAppliedRV = o.resourceVersion
}

func (o *schedulerService) operatingPower(from, to time.Time, limit int) (serving.OperatingPowerWindow, error) {
	// Capture one accepted configuration without waiting behind refresh or Tick.
	config := o.configuration.Load()
	meters := make([]serving.OperatingPowerMeter, len(config.Meters))
	for index, meter := range config.Meters {
		meters[index] = serving.OperatingPowerMeter{
			Node: meter.Node, Meter: meter.ID, Scope: meter.Scope, Source: meter.Source,
		}
	}
	return o.observationLog.OperatingPower(from, to, meters, limit)
}

func (o *schedulerService) recordDecision(decision serving.SchedulerDecision) error {
	snapshot := o.controller.State().Snapshot
	return o.observationLog.SchedulerRecord(serving.SchedulerObservation{
		At: decision.At, Decision: &decision, Snapshot: &snapshot,
	})
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
	}
	if !result.EndedAt.IsZero() {
		event.CompletedAt = &result.EndedAt
	}
	return o.observationLog.SchedulerRecord(serving.SchedulerObservation{At: time.Now().UTC(), Action: &event})
}

func (o *schedulerService) restartPower(config schedulerConfiguration) {
	if o.powerCancel != nil {
		o.powerCancel()
		o.powerWG.Wait()
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
