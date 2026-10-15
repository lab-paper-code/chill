package main

import (
	"bytes"
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
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/metricobserver/edgemetrics"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
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
	Preparations     map[string]servingkube.PrepareSpec     `json:"preparations"`
	CostReferences   []evidencestore.ObjectIdentity         `json:"costReferences"`
	PowerCostSources []servingkube.PowerCostSource          `json:"powerCostSources"`
	Meters           []servingkube.MeterDeclaration         `json:"meters"`
	PowerRequests    map[string]*edge.NodeActuation         `json:"powerRequests"`
	PowerObservation *powerObservationConfig                `json:"powerObservation,omitempty"`
	StaticRouting    map[string]map[string]int              `json:"staticRouting,omitempty"`
}

type schedulerOwner struct {
	lock                      sync.Mutex
	controller                *servingkube.SchedulerController
	operations                *servingkube.Operations
	store                     *servingkube.Store
	kube                      kubernetes.Interface
	execution                 *serving.Runtime
	observer                  *servingkube.Observer
	objects                   *stores3.Store
	journal                   *serving.Journal
	opts                      options
	resourceVersion, profiles string
	staticAppliedRV           string
	configuration             schedulerConfiguration
	ctx                       context.Context
	cancel                    context.CancelFunc
	powerCancel               context.CancelFunc
	wg                        sync.WaitGroup
	powerWG                   sync.WaitGroup
}

func newSchedulerOwner(ctx context.Context, store *servingkube.Store, kube kubernetes.Interface,
	execution *serving.Runtime, observer *servingkube.Observer, objects *stores3.Store,
	journal *serving.Journal, opts options,
) (*schedulerOwner, error) {
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
	ownerCtx, cancel := context.WithCancel(ctx)
	owner := &schedulerOwner{
		operations: operations, store: store, kube: kube, execution: execution, observer: observer,
		objects: objects, journal: journal, opts: opts, ctx: ownerCtx, cancel: cancel,
	}
	initializeCtx, cancelInitialize := context.WithTimeout(ctx, opts.requestTimeout)
	defer cancelInitialize()
	if err = owner.refresh(initializeCtx); err != nil {
		owner.Close()
		return nil, err
	}
	return owner, nil
}

func decodeSchedulerConfiguration(raw string, maxBytes int64) (schedulerConfiguration, error) {
	var config schedulerConfiguration
	if int64(len(raw)) > maxBytes {
		return config, errors.New("scheduler configuration exceeds body bound")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return config, errors.New("one scheduler configuration object required")
	}
	if err := config.Settings.Validate(); err != nil {
		return config, err
	}
	if err := config.validateInventory(); err != nil {
		return config, err
	}
	if err := config.validatePower(); err != nil {
		return config, err
	}
	for profile, preparation := range config.Preparations {
		if profile == "" || preparation.Instance.Profile != nil {
			return config, errors.New("preparations select verified catalog profile IDs, not embedded profiles")
		}
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
		if p.MaximumBoundaryDistance <= 0 || p.MaximumGap <= 0 ||
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

func (o *schedulerOwner) loadCompletionCalibrations(
	ctx context.Context, preparations map[string]servingkube.PrepareSpec,
) (
	map[string]map[string]serving.ObservedOverhead, map[string]string,
) {
	calibrations := make(map[string]map[string]serving.ObservedOverhead)
	failures := make(map[string]string)
	// Reuse shared raw cohorts only within this refresh; no persistent cache.
	type calibrationLoad struct {
		selection *serving.CompletionCalibration
		facts     map[string]serving.ObservedOverhead
		err       error
	}
	loads := make([]calibrationLoad, 0, len(preparations))
	for profile, preparation := range preparations {
		selection := preparation.CompletionCalibration
		if selection == nil {
			continue
		}
		var loaded *calibrationLoad
		for index := range loads {
			prior := &loads[index]
			if prior.selection.From.Equal(selection.From) && prior.selection.To.Equal(selection.To) &&
				slices.Equal(prior.selection.Windows, selection.Windows) {
				loaded = prior
				break
			}
		}
		if loaded == nil {
			facts, err := serving.LoadCompletionCalibration(ctx, o.objects, *selection, o.opts.maxEvidenceBytes)
			loads = append(loads, calibrationLoad{selection: selection, facts: facts, err: err})
			loaded = &loads[len(loads)-1]
		}
		if loaded.err != nil {
			failures[profile] = loaded.err.Error()
			continue
		}
		matching := make(map[string]serving.ObservedOverhead)
		for id, fact := range loaded.facts {
			if fact.Matches(preparation.Instance) {
				matching[id] = fact
			}
		}
		if len(matching) == 0 {
			failures[profile] = "completion calibration lacks matching measured execution conditions"
		} else {
			calibrations[profile] = matching
		}
	}
	return calibrations, failures
}

func (o *schedulerOwner) refresh(ctx context.Context) error {
	o.lock.Lock()
	defer o.lock.Unlock()
	cm, err := o.kube.CoreV1().ConfigMaps(o.opts.servingNamespace).Get(ctx, o.opts.schedulerConfigMap, meta.GetOptions{})
	if err != nil {
		return err
	}
	profiles := o.observer.Profiles()
	ids := make([]string, len(profiles))
	for i, p := range profiles {
		ids[i] = p.ID
	}
	slices.Sort(ids)
	signature := strings.Join(ids, "\x00")
	if o.controller != nil && cm.ResourceVersion == o.resourceVersion && signature == o.profiles {
		o.applyStaticRouting(ctx)
		return nil
	}
	config, err := decodeSchedulerConfiguration(cm.Data[o.opts.schedulerConfigKey], o.opts.maxRequestBytes)
	if err != nil {
		return err
	}
	costs, baselines, err := servingkube.LoadOperationMeasurements(ctx, o.objects,
		config.CostReferences, o.opts.maxEvidenceBytes)
	if err != nil {
		return err
	}
	powerCosts, err := o.operations.LoadPowerCosts(ctx, o.objects, config.PowerCostSources, o.opts.maxEvidenceBytes)
	if err != nil {
		return err
	}
	costs = append(costs, powerCosts...)
	preparations := make(map[string]servingkube.PrepareSpec, len(config.Preparations))
	for _, profile := range profiles {
		preparation, selected := config.Preparations[profile.ID]
		if !selected {
			continue
		}
		preparation.Instance.Profile = &profile
		if preparation.Instance.Node.UID != profile.Condition.NodeUID || preparation.Instance.Node.Name != profile.NodeName {
			return fmt.Errorf("profile %s preparation Node differs from measured condition", profile.ID)
		}
		if preparation.Store.Store != o.opts.storeName || preparation.Store.Endpoint != o.opts.storeEndpoint ||
			preparation.Store.Bucket != o.opts.storeBucket || preparation.Store.Region != o.opts.storeRegion {
			return errors.New("preparation store differs from configured immutable profile store")
		}
		if err = preparation.Store.Validate(); err != nil {
			return err
		}
		bundle := native.ProfileBundle{ProfileID: profile.ID, Batches: make([]native.BatchProfile, len(profile.Batches))}
		for i, batch := range profile.Batches {
			samples := make([]float64, len(batch.DurationNanos))
			for j, value := range batch.DurationNanos {
				samples[j] = value / float64(time.Millisecond)
			}
			slices.Sort(samples)
			bundle.Batches[i] = native.BatchProfile{
				Size: batch.Batch, ServiceSamplesMS: samples, EnergyPerItemJ: batch.EnergyPerInputJoules,
			}
		}
		raw, err := json.Marshal(bundle)
		if err != nil {
			return err
		}
		if int64(len(raw)) > o.opts.maxEvidenceBytes {
			return errors.New("verified profile bundle exceeds immutable object bound")
		}
		identity := evidencestore.ObjectIdentity{
			Store: o.opts.storeName, Key: "serving/profiles/" + profile.ID + "/bundle.json", Size: int64(len(raw)),
		}
		if _, err = o.objects.PutImmutable(ctx, identity, bytes.NewReader(raw)); err != nil {
			return err
		}
		preparation.Bundle = identity
		preparation.Config.Mode = "serving"
		preparation.Config.ProfilePath = "/profile/bundle.json"
		preparation.Config.Policy.ProfileID = profile.ID
		preparation.Config.Policy.Batches = nil
		preparations[profile.ID] = preparation
	}
	calibrations, calibrationErrors := o.loadCompletionCalibrations(ctx, preparations)
	controllerConfig := servingkube.SchedulerControllerConfig{
		Settings: config.Settings, CPUInventory: config.CPUInventory, GPUInventory: config.GPUInventory,
		Reserved: config.Reserved, Preparations: preparations, Costs: costs, Baselines: baselines,
		Meters: config.Meters, PowerRequests: config.PowerRequests,
		CompletionCalibrations: calibrations, CalibrationErrors: calibrationErrors,
		StaticRouting: config.StaticRouting != nil,
		OnDecision:    o.recordDecision, OnAction: o.recordAction,
	}
	if o.controller == nil {
		o.controller, err = servingkube.NewSchedulerController(o.store, o.kube, o.execution,
			o.observer, o.operations, o.journal, controllerConfig)
	} else {
		err = o.controller.SetConfiguration(controllerConfig)
	}
	if err != nil {
		return err
	}
	o.configuration = config
	o.resourceVersion = cm.ResourceVersion
	o.profiles = signature
	o.restartPower(config)
	o.applyStaticRouting(ctx)
	return nil
}

// An acknowledged configuration can await actual physical readiness. The normal
// refresh cadence applies its manual baseline once, never while controls are ON.
func (o *schedulerOwner) applyStaticRouting(ctx context.Context) {
	if o.configuration.StaticRouting == nil {
		o.staticAppliedRV = ""
		return
	}
	if o.configuration.Settings.Controls.Enabled || o.staticAppliedRV == o.resourceVersion {
		return
	}
	if err := o.controller.ApplyStaticRouting(ctx, o.configuration.StaticRouting, o.resourceVersion); err != nil {
		ctrl.Log.Error(err, "manual staticRouting remains unapplied", "sourceConfigRV", o.resourceVersion)
		return
	}
	o.staticAppliedRV = o.resourceVersion
}

func (o *schedulerOwner) operatingPower(from, to time.Time, limit int) (serving.OperatingPowerWindow, error) {
	o.lock.Lock()
	meters := make([]serving.OperatingPowerMeter, len(o.configuration.Meters))
	for index, meter := range o.configuration.Meters {
		meters[index] = serving.OperatingPowerMeter{
			Node: meter.Node, Meter: meter.ID, Scope: meter.Scope, Source: meter.Source,
		}
	}
	o.lock.Unlock()
	return o.journal.OperatingPower(from, to, meters, limit)
}

func (o *schedulerOwner) recordDecision(decision serving.SchedulerDecision) error {
	snapshot := o.controller.State().Snapshot
	return o.journal.SchedulerRecord(serving.SchedulerObservation{
		At: decision.At, Decision: &decision, Snapshot: &snapshot,
	})
}
func (o *schedulerOwner) recordAction(result servingkube.OperationResult) error {
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
	return o.journal.SchedulerRecord(serving.SchedulerObservation{At: time.Now().UTC(), Action: &event})
}

func (o *schedulerOwner) restartPower(config schedulerConfiguration) {
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
				if err = o.journal.SchedulerRecord(event); err != nil {
					ctrl.Log.Error(err, "persist scheduler whole-node power observation")
					return
				}
			}
		}(meter)
	}
}

func (o *schedulerOwner) Start() {
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
func (o *schedulerOwner) Close() {
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
