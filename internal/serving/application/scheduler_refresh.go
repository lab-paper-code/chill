package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// acquireRefresh takes the refresh lock, or reports false so a tick is skipped
// while a measurement deletion holds it and the caller's loop is not delayed.
// The first refresh waits because nothing is accepted before it.
func (o *SchedulerService) acquireRefresh() bool {
	if o.RefreshLock.TryLock() {
		return true
	}
	if o.Profiles.Load() != nil {
		return false
	}
	o.RefreshLock.Lock()
	return true
}

// Refresh serializes source preparation separately from Tick and user mutations.
// Only complete, still-current inputs may replace the accepted configuration.
func (o *SchedulerService) Refresh(ctx context.Context) (err error) {
	if !o.acquireRefresh() {
		return nil
	}
	defer o.RefreshLock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	cm, err := o.kube.CoreV1().ConfigMaps(o.opts.ServingNamespace).Get(ctx, o.opts.SchedulerConfigMap, meta.GetOptions{})
	if err != nil {
		return err
	}
	config, err := DecodeSchedulerConfiguration(cm.Data[o.opts.SchedulerConfigKey], o.opts.MaxRequestBytes)
	if err != nil {
		return err
	}
	policies, factsVersion, err := o.profilerFacts(ctx, &config)
	if err != nil {
		return err
	}
	if config.PowerObservation != nil {
		limits := metricobserver.Limits{MaxResultBytes: o.opts.MaxEvidenceBytes, MemoryBudgetBytes: debug.SetMemoryLimit(-1)}
		for _, meter := range config.Meters {
			if _, err := metricobserver.RequiredCapacity(
				config.PowerObservation.Sampling, meter.Source, limits, false,
			); err != nil {
				return fmt.Errorf("admit scheduler power observation for %s: %w", meter.ID, err)
			}
		}
	}
	namespaces := o.opts.ProfileNamespaces
	if len(namespaces) == 0 {
		namespaces = []string{o.opts.ServingNamespace}
	}
	view, err := o.catalog.Refresh(ctx, namespaces)
	if err != nil {
		return err
	}
	if err = o.retainProfileReuseChecks(ctx, &view); err != nil {
		return err
	}
	// Coverage consumes verified measurement facts independently of operation
	// costs and scheduler preparation/adoption. No raw evidence is read here.
	o.catalogRead.Store(&ProfilesView{ConfigVersion: cm.ResourceVersion, CatalogView: view})
	costs, err := o.costs.Refresh(ctx, namespaces, view.Profiles)
	if err != nil {
		return err
	}
	occupancy, skipped := o.costs.OccupancySelections(costs.DispatchOccupancy, view.Profiles)
	signature, err := refreshSourceSignature(config, view, costs, factsVersion)
	if err != nil {
		return err
	}
	currentProfiles := view.CurrentProfiles()
	o.Lock.Lock()
	accepted := o.Profiles.Load()
	sameInputs := o.Controller != nil && cm.ResourceVersion == o.resourceVersion && signature == o.sources && accepted != nil
	o.Lock.Unlock()
	if sameInputs && len(accepted.Preparations) == len(currentProfiles) {
		return ctx.Err()
	}
	preparations, issues, err := o.preparations(ctx, config, currentProfiles)
	if err != nil {
		return err
	}
	view.Insufficiencies = append(view.Insufficiencies, issues...)
	// Build the registered services' quantile tables now, outside the lock and
	// before any control round queries these profiles.
	serving.PrepareServiceQuantiles(o.execution.SchedulerInputs(time.Now).Workloads, view.Profiles)
	o.Lock.Lock()
	defer o.Lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// A PUT can accept policy while source IO is in flight. Re-read the existing
	// optimistic version at the swap so that older preparation cannot undo it.
	current, err := o.kube.CoreV1().ConfigMaps(o.opts.ServingNamespace).Get(ctx, o.opts.SchedulerConfigMap, meta.GetOptions{})
	if err != nil {
		return err
	}
	if current.ResourceVersion != cm.ResourceVersion {
		return errors.New("scheduler configuration changed during refresh")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if sameInputs && reflect.DeepEqual(view.Insufficiencies, accepted.Insufficiencies) &&
		maps.EqualFunc(preparations, accepted.Preparations, func(spec, published servingstate.PrepareSpec) bool {
			spec.Instance.Profile = nil // compare the accepted projection without changing the controller spec
			return reflect.DeepEqual(spec, published)
		}) {
		return nil
	}
	controllerConfig := servingkube.SchedulerControllerConfig{
		Settings:          config.Settings,
		ConfigurationName: o.opts.SchedulerConfigMap, ConfigurationKey: o.opts.SchedulerConfigKey,
		Reserved: config.Reserved, Preparations: preparations, Costs: costs.Costs, Baselines: costs.Baselines,
		CostIssues: costs.Issues, DispatchOccupancy: costs.DispatchOccupancy, CatalogIssues: view.Insufficiencies,
		DispatchOccupancySnapshot: o.occupancy.selected,
		LoadGenerator:             o.opts.LoadGenerator,
		Session:                   o.opts.Session,
		GatewayRequestTimeout:     o.GatewayRequestTimeout,
		Meters:                    config.Meters, PowerPolicies: policies,
		StaticRouting: len(config.StaticRouting) > 0, MeasurementNamespaces: namespaces,
		Evidence: o.objects, EvidenceStore: o.opts.StoreName, MeterSource: metricSource,
		OnDecision: o.RecordDecision, OnAction: o.RecordAction,
	}
	if o.Controller == nil {
		o.Controller, err = servingkube.NewSchedulerController(o.cluster, o.store, o.kube, o.execution,
			o.observer, o.operations, o.observationLog, controllerConfig)
	} else {
		err = o.Controller.SetConfiguration(controllerConfig)
	}
	if err != nil {
		return err
	}
	// Publish the complete configuration only after the controller accepted it.
	o.observer.SetProfiles(view.Profiles)
	o.execution.SetCompletionMargin(config.Settings.CompletionMarginMS)
	o.configuration.Store(&config)
	published := ProfilesView{
		ConfigVersion: cm.ResourceVersion, CatalogView: view,
		Preparations: make(map[string]servingstate.PrepareSpec, len(preparations)),
		Occupancy:    occupancy, OccupancySkipped: skipped,
	}
	for id, spec := range preparations {
		spec.Instance.Profile = nil // the profile body is in Profiles
		published.Preparations[id] = spec
	}
	o.Profiles.Store(&published)
	o.occupancy.configure(&rollingOccupancyInput{
		configuration: &config, catalog: &published, profiles: o.observer.OccupancyProfiles(),
		legacy: costs.DispatchOccupancy,
	})
	o.resourceVersion = cm.ResourceVersion
	o.sources = signature
	o.restartPower(config)
	return nil
}

func refreshSourceSignature(config SchedulerConfiguration, view servingkube.CatalogView, costs servingkube.CostView, factsVersion string) (string, error) {
	sources := costs.Sources
	sources = append(sources, "profiler-facts:"+factsVersion)
	for _, meter := range config.Meters {
		sources = append(sources, "meter:"+meter.ID+"/"+meter.Node.UID)
	}
	for _, p := range view.Profiles {
		sources = append(sources, p.ID)
	}
	for _, measurement := range view.CurrentMeasurements {
		sources = append(sources, "current:"+measurement.ID+"/"+measurement.MeasuredAt.Format(time.RFC3339Nano))
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
			return "", err
		}
		sources = append(sources, "profile-reuse:"+string(checks))
	}
	slices.Sort(sources)
	signature := strings.Join(sources, "\x00")
	return signature, nil
}
