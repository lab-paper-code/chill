package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/serving/measurement"

	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

type powerObservationConfig struct {
	Sampling metricobserver.Request `json:"sampling"`
	Coverage powerwindow.Policy     `json:"coverage"`
}

type SchedulerConfiguration struct {
	Settings serving.SchedulerSettings              `json:"settings"`
	Reserved map[string]serving.SchedulerAllocation `json:"reserved"`
	// Meters are derived from the Profiler metric sources, never configured.
	Meters            []servingops.MeterDeclaration        `json:"-"`
	PowerObservation  *powerObservationConfig              `json:"powerObservation,omitempty"`
	DispatchOccupancy *measurement.DispatchOccupancyPolicy `json:"dispatchOccupancy,omitempty"`
	StaticRouting     map[string]map[string]int            `json:"staticRouting,omitempty"`
	// StaticRoutingValidatedAt names the settings change that wrote StaticRouting.
	StaticRoutingValidatedAt *time.Time `json:"staticRoutingValidatedAt,omitempty"`
}

type SchedulerService struct {
	Lock                     sync.Mutex
	refreshLock              sync.Mutex
	Controller               *servingkube.SchedulerController
	cluster                  *servingkube.ClusterState
	operations               *servingops.Operations
	store                    *servingkube.Store
	kube                     kubernetes.Interface
	settingsKube             kubernetes.Interface
	execution                *serving.Runtime
	observer                 *servingkube.Observer
	objects                  *stores3.Store
	observationLog           *observation.ObservationLog
	live                     *Live
	opts                     Options
	GatewayRequestTimeout    time.Duration
	catalog                  *servingkube.Catalog
	costs                    *servingkube.CostEvidence
	resourceVersion, sources string
	configuration            atomic.Pointer[SchedulerConfiguration]
	Profiles                 atomic.Pointer[ProfilesView]
	catalogRead              atomic.Pointer[ProfilesView]
	occupancy                rollingOccupancyCache
	ctx                      context.Context
	cancel                   context.CancelFunc
	powerCancel              context.CancelFunc
	wg                       sync.WaitGroup
	powerWG                  sync.WaitGroup
}

func NewSchedulerService(ctx context.Context, dependencies Dependencies) *SchedulerService {
	serviceCtx, cancel := context.WithCancel(ctx)
	return &SchedulerService{
		cluster:    dependencies.Cluster,
		operations: dependencies.Operations, store: dependencies.Store, kube: dependencies.Kube,
		settingsKube: dependencies.SettingsKube,
		execution:    dependencies.Execution, observer: dependencies.Observer, objects: dependencies.Objects,
		observationLog: dependencies.ObservationLog, opts: dependencies.Options, ctx: serviceCtx, cancel: cancel,
		GatewayRequestTimeout: dependencies.GatewayRequestTimeout, live: dependencies.Live,
		catalog: dependencies.Catalog, costs: dependencies.Costs,
	}
}

func DecodeSchedulerConfiguration(raw string, maxBytes int64) (SchedulerConfiguration, error) {
	// The wire settings field shadows the embedded one, so an omitted margin stays distinguishable from zero.
	var wire struct {
		SchedulerConfiguration
		Settings struct {
			serving.SchedulerSettingsInput
			DefaultDispatchOccupancyNanos *int64 `json:"defaultDispatchOccupancyNanos"`
		} `json:"settings"`
	}
	if int64(len(raw)) > maxBytes {
		return SchedulerConfiguration{}, errors.New("scheduler configuration exceeds body bound")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return SchedulerConfiguration{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SchedulerConfiguration{}, errors.New("one scheduler configuration object required")
	}
	config := wire.SchedulerConfiguration
	if wire.Settings.DefaultDispatchOccupancyNanos == nil {
		return config, errors.New("settings.defaultDispatchOccupancyNanos required")
	}
	wire.Settings.SchedulerSettings.DefaultDispatchOccupancyNanos = *wire.Settings.DefaultDispatchOccupancyNanos
	var err error
	if config.Settings, err = wire.Settings.Settings(); err != nil {
		return config, err
	}
	if err := config.validatePower(); err != nil {
		return config, err
	}
	if config.DispatchOccupancy != nil {
		if err := config.DispatchOccupancy.Validate(); err != nil {
			return config, err
		}
	}
	return config, nil
}

func (config SchedulerConfiguration) validatePower() error {
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

func (o *SchedulerService) measurementCatalogView(_ context.Context) (ProfilesView, error) {
	published := o.catalogRead.Load()
	if published == nil {
		return ProfilesView{}, &serving.Error{
			Code: "CoverageWarming", Message: "measurement catalog is not available",
		}
	}
	view := *published
	checked, sourceError, revision := o.catalog.SourceStatus()
	if revision == view.Revision {
		view.SourceCheckedAt, view.SourceError = checked, sourceError
	} else {
		view.SourceError = "CatalogPublicationPending"
		if sourceError != "" {
			view.SourceError = sourceError
		}
	}
	return view, nil
}

// profilerFacts reads the configured actuation policies and derives the wall
// meters from the Profiler facts. Without the Profiler there are none: no Node
// takes power requests and no meter is declared. A meter binds to the live
// Node of its name; a source whose Node is absent declares no meter.
func (o *SchedulerService) profilerFacts(
	ctx context.Context, config *SchedulerConfiguration,
) ([]nodeactuator.ActuationPolicySpec, string, error) {
	if o.opts.FactsConfigMap == "" {
		return nil, "", nil
	}
	cm, err := o.kube.CoreV1().ConfigMaps(o.opts.FactsNamespace).Get(ctx, o.opts.FactsConfigMap, meta.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	policies, err := profiler.DecodeActuationPolicyCatalog([]byte(cm.Data[o.opts.ActuationPoliciesKey]))
	if err != nil {
		return nil, "", err
	}
	sources, err := profilerkube.DecodeMetricSources([]byte(cm.Data[o.opts.MetricSourcesKey]))
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
			config.Meters = append(config.Meters, servingops.MeterDeclaration{
				ID: s.MeterID, Node: serving.NodeReference{Name: s.Node, UID: uid}, Scope: "node-exclusive-wall",
				Source: metricobserver.SourceIdentity{
					Kind: s.Kind, NodeName: s.Node, Endpoint: s.Endpoint, Metric: s.PowerMetric, MetricLabels: s.MetricLabels,
				},
			})
		}
	}
	return policies, cm.ResourceVersion, nil
}

// AcceptedSettings returns the last accepted user policy without waiting behind
// refresh or Tick; false before the first configuration acceptance.
func (o *SchedulerService) AcceptedSettings() (serving.SchedulerSettings, bool) {
	config := o.configuration.Load()
	if config == nil {
		return serving.SchedulerSettings{}, false
	}
	return config.Settings.Clone(), true
}

// schedulerMeters projects only the complete configuration accepted by refresh.
func (o *SchedulerService) SchedulerMeters() []servingops.MeterDeclaration {
	config := o.configuration.Load()
	if config == nil {
		return nil
	}
	return config.Meters
}

func (o *SchedulerService) OperatingPower(
	ctx context.Context, from, to time.Time, limit int,
) (observation.OperatingPowerWindow, error) {
	return o.observationLog.OperatingPower(ctx, from, to, o.operatingPowerMeters(), limit)
}

func (o *SchedulerService) OperatingPowerSeries(
	ctx context.Context, from, to time.Time, bucket time.Duration, limit int,
) (observation.OperatingPowerSeries, error) {
	return o.observationLog.OperatingPowerSeries(ctx, from, to, bucket, o.operatingPowerMeters(), limit)
}

func (o *SchedulerService) operatingPowerMeters() []observation.OperatingPowerMeter {
	// Capture one accepted configuration without waiting behind refresh or Tick.
	config := o.configuration.Load()
	meters := make([]observation.OperatingPowerMeter, len(config.Meters))
	for index, meter := range config.Meters {
		meters[index] = observation.OperatingPowerMeter{
			Node: meter.Node, Meter: meter.ID, Scope: meter.Scope, Source: meter.Source,
		}
	}
	return meters
}

func (o *SchedulerService) RecordDecision(
	decision serving.SchedulerDecision, snapshot serving.SchedulerSnapshot,
) error {
	if err := o.observationLog.SchedulerRecord(observation.SchedulerObservation{
		At: decision.At, Decision: &decision, Snapshot: &snapshot,
	}); err != nil {
		return err
	}
	if o.live != nil {
		o.live.Decision(decision, snapshot)
	}
	return nil
}
func (o *SchedulerService) RecordAction(result servingstate.OperationResult) error {
	refs := &observation.SchedulerReferences{}
	operation := result.Operation
	if operation.Target != nil {
		target := refs.Candidate(*operation.Target)
		operation.Target = &target
	}
	if operation.Prepare != nil {
		prepare := *operation.Prepare
		prepare.Instance = refs.Instance(prepare.Instance)
		operation.Prepare = &prepare
	}
	result.Operation = operation
	expected, err := json.Marshal(result.Operation)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(result)
	if err != nil {
		return err
	}
	event := observation.SchedulerActionObservation{
		ID: result.Operation.ID, Kind: result.Operation.Kind, Node: result.Operation.Node,
		Instance: result.Operation.Instance, Phase: result.Status, StartedAt: result.StartedAt,
		Expected: expected, Actual: actual, Error: result.Error,
		ExpectedSeconds: result.Operation.ExpectedSeconds, ExpectedSource: result.Operation.ExpectedSource,
	}
	if !result.EndedAt.IsZero() {
		event.CompletedAt = &result.EndedAt
	}
	recordedAt := time.Now().UTC()
	if err := o.observationLog.SchedulerRecord(observation.SchedulerObservation{
		At: recordedAt, Action: &event, References: refs,
	}); err != nil {
		return err
	}
	if o.live != nil {
		o.live.Action(event, recordedAt)
	}
	return nil
}

func (o *SchedulerService) restartPower(config SchedulerConfiguration) {
	if o.powerCancel != nil {
		o.powerCancel()
		o.powerWG.Wait()
	}
	if o.live != nil {
		o.live.PowerScope(config.Meters, config.PowerObservation != nil)
	}
	if config.PowerObservation == nil {
		return
	}
	ctx, cancel := context.WithCancel(o.ctx)
	limits := metricobserver.Limits{MaxResultBytes: o.opts.MaxEvidenceBytes, MemoryBudgetBytes: debug.SetMemoryLimit(-1)}
	o.powerCancel = cancel
	for _, meter := range config.Meters {
		o.powerWG.Add(1)
		go func(meter servingops.MeterDeclaration) {
			defer o.powerWG.Done()
			source, err := metricSource(meter.Source)
			if err != nil {
				ctrl.Log.Error(err, "configure scheduler wall meter")
				return
			}
			if o.live != nil {
				source = o.live.MeterSource(source, meter.Node.UID)
			}
			if _, err := metricobserver.RequiredCapacity(
				config.PowerObservation.Sampling, source.Identity(), limits, false,
			); err != nil {
				ctrl.Log.Error(err, "admit scheduler power observation")
				return
			}
			observer, err := metricobserver.New(source, limits)
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
				event := observation.SchedulerObservation{
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

// controlRound measures periodic admission and evaluation. The application lock
// protects controller lookup, never the long-running control call.
func (o *SchedulerService) controlRound(ctx context.Context) time.Time {
	started := time.Now()
	event := observation.SchedulerControlObservation{Scope: "round", StartedAt: started.UTC(), Phase: "complete"}
	o.Lock.Lock()
	controller := o.Controller
	o.Lock.Unlock()
	admitStarted := time.Now()
	event.LockWait = admitStarted.Sub(started)
	if err := controller.Admit(ctx); err != nil {
		event.AdmitError = err.Error()
		ctrl.Log.Error(err, "admit measurements")
	}
	tickStarted := time.Now()
	event.Admit = tickStarted.Sub(admitStarted)
	event.TickAt = tickStarted.UTC()
	_, err := controller.Tick(ctx, event.TickAt)
	event.Tick = time.Since(tickStarted)
	finished := time.Now()
	event.FinishedAt, event.Elapsed = finished.UTC(), finished.Sub(started)
	if err != nil {
		event.Error = err.Error()
		ctrl.Log.Error(err, "evaluate and record scheduler control interval")
	}
	if recordErr := o.observationLog.SchedulerRecord(observation.SchedulerObservation{At: event.FinishedAt, Control: &event}); recordErr != nil {
		ctrl.Log.Error(recordErr, "record scheduler control round completion")
	}
	return started
}

func (o *SchedulerService) Start() {
	if o == nil {
		return
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.occupancy.run(o.ctx, o.aggregateOccupancy)
	}()
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		for o.ctx.Err() == nil {
			callCtx, cancel := context.WithTimeout(o.ctx, o.opts.RequestTimeout)
			// Control uses the last accepted inputs independently of refresh.
			started := o.controlRound(callCtx)
			cancel()
			timer := time.NewTimer(max(time.Until(started.Add(o.Controller.Settings().ControlInterval)), 0))
			select {
			case <-o.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		for {
			select {
			case <-o.ctx.Done():
				return
			case <-o.Controller.Completions():
				callCtx, cancel := context.WithTimeout(o.ctx, o.opts.RequestTimeout)
				err := o.Controller.Continue(callCtx, time.Now().UTC())
				cancel()
				if err != nil {
					ctrl.Log.Error(err, "continue authorized scheduler transition")
				}
			}
		}
	}()
}
func (o *SchedulerService) Close() {
	if o == nil {
		return
	}
	o.cancel()
	o.wg.Wait()
	if o.powerCancel != nil {
		o.powerCancel()
	}
	o.powerWG.Wait()
	if o.Controller != nil {
		o.Controller.Close()
	}
}
