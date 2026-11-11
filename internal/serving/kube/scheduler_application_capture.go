package kube

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *SchedulerController) capture(ctx context.Context, now time.Time, config SchedulerControllerConfig) (serving.SchedulerSnapshot, error) {
	return c.captureTimed(ctx, now, config, nil, true)
}

func (c *SchedulerController) captureTimed(ctx context.Context, now time.Time, config SchedulerControllerConfig, timing *observation.SchedulerControlObservation, demand bool) (serving.SchedulerSnapshot, error) {
	var window observation.LoadWindow
	if demand {
		windowStarted := time.Now()
		var err error
		window, err = c.observationLog.LoadWindowTimed(ctx, now.Add(-config.Settings.ObservationWindow), now, timing)
		if timing != nil {
			timing.Window = time.Since(windowStarted)
		}
		if err != nil {
			return serving.SchedulerSnapshot{}, err
		}
		start := serving.LaterTime(window.From, c.observationLog.Started())
		if err := schedulerRequestCoverageError(window.Coverage.Gaps, start, now); err != nil {
			return serving.SchedulerSnapshot{}, err
		}
	}
	s := serving.SchedulerSnapshot{ObservationEnd: now, ObservedSince: c.observationLog.Started(), Arrivals: window.Arrivals, Costs: config.Costs, Baselines: config.Baselines,
		DispatchOccupancy: config.DispatchOccupancy}
	if config.DispatchOccupancySnapshot != nil {
		s.DispatchOccupancy = config.DispatchOccupancySnapshot()
	}
	// Observer state is read before the Node list: another refresh holding the
	// observer lock would otherwise age every Node.
	profiles, profileByID := c.observer.schedulerProfiles()
	reboots := c.observer.powerRebootFacts()
	// Historical reads must finish before refreshing authoritative live sources.
	refreshed, err := c.observer.refresh(ctx)
	if timing != nil {
		timing.RefreshList = refreshed.listNanos
		timing.RefreshNodes = refreshed.nodesNanos
		timing.RefreshPods = refreshed.podsNanos
		timing.RefreshResources = refreshed.resourcesNanos
		timing.RefreshDeviceClasses = refreshed.classesNanos
		timing.RefreshServices = refreshed.servicesNanos
		timing.SourceProbe = refreshed.sourceProbeNanos
	}
	if err != nil {
		return s, err
	}
	nodes, pods, classes := refreshed.nodes, refreshed.clusterPods, refreshed.classes
	nodesObservedAt := refreshed.nodesObservedAt
	if pods == nil {
		// A namespace-only observer cannot prove occupancy across the POOL.
		listStarted := time.Now()
		pods, err = c.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{FieldSelector: nonTerminalPodSelector})
		if timing != nil {
			elapsed := time.Since(listStarted)
			timing.RefreshList += elapsed
			timing.RefreshPods += elapsed
		}
		if err != nil {
			return s, err
		}
	} else {
		// Keep the scheduler's nonterminal projection separate from OnCluster's
		// complete baseline and the observer's exact terminal executor ledger.
		pods.Items = slices.DeleteFunc(pods.Items, func(p core.Pod) bool {
			return p.Status.Phase == core.PodSucceeded || p.Status.Phase == core.PodFailed
		})
	}
	power := map[string]time.Duration{}
	physical := map[string]serving.InstanceState{}
	physicalStates := c.runtime.Instances()
	for _, state := range physicalStates {
		key := state.ID
		if key == "" {
			key = state.PodUID
		}
		physical[key] = state
	}
	// The entire POOL is every Node without the control-plane role.
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if _, controlPlane := n.Labels[controlPlaneLabel]; controlPlane {
			continue
		}
		sn := serving.SchedulerNode{Node: serving.NodeReference{Name: n.Name, UID: string(n.UID)}, Measurement: profilingrun.InMeasurement(n), Reserved: config.Reserved[n.Name], MemoryBytes: uint64(n.Status.Allocatable.Memory().Value()), ObservedAt: nodesObservedAt}
		sn.Reboots = reboots[sn.Node]
		for _, condition := range n.Status.Conditions {
			if condition.Type == core.NodeReady {
				sn.Ready = condition.Status == core.ConditionTrue
			}
		}
		if slices.Contains(config.Settings.ExcludedNodes, n.Name) {
			sn.Reasons = append(sn.Reasons, serving.NodeExcluded)
		}
		captureNodeInventory(&sn, n, profiles)
		sn.PowerMode = servingstate.ReadBackMode(n, servingstate.NodeClass(n, classes.Items))
		lowest := serving.SchedulerLowestMode(config.Baselines, sn.Node)
		targets := nodePowerTargets(n, classes.Items, config.PowerPolicies)
		if _, ok := targets[lowest]; sn.PowerMode == "" || len(targets) == 0 || lowest != "" && lowest != sn.PowerMode && !ok {
			sn.Reasons = append(sn.Reasons, serving.NodePowerModeUnavailable)
			if sn.PowerMode == "" {
				sn.Reasons = append(sn.Reasons, serving.NodePowerModeObservationUnavailable)
			} else {
				sn.Reasons = append(sn.Reasons, serving.NodePowerTransitionUnavailable)
			}
		}
		for mode, maxDuration := range targets {
			power[sn.Node.UID+"/"+mode] = maxDuration
		}
		captureNodePods(&s, &sn, n, pods, physical, profileByID)
		s.Nodes = append(s.Nodes, sn)
	}
	slices.SortFunc(s.Instances, func(a, b serving.SchedulerInstance) int {
		return strings.Compare(a.ID+a.PodUID, b.ID+b.PodUID)
	})
	c.mu.Lock()
	c.powerTargets = power
	c.mu.Unlock()
	current := c.runtime.SchedulerInputs(time.Now)
	s.At, s.Workloads, s.Outstanding = current.At, current.Workloads, current.Outstanding
	s.Routing, s.RoutingCredits = current.Routing, current.RoutingCredits
	capturePathFeedback(&s, window.PathSamples, config.Settings.CompletionMarginMS)
	capturePreparationOptions(&s, profiles, profileByID, config.Preparations, now)
	bindProfileMeters(&s, config.Meters)
	if err := ctx.Err(); err != nil {
		return s, err
	}
	c.releaseObservedScopes(nodes.Items, pods.Items, physicalStates, now)
	return s, nil
}

func capturePreparationOptions(s *serving.SchedulerSnapshot, profiles []serving.Profile, profileByID map[string]*serving.Profile, preparations map[string]servingstate.PrepareSpec, now time.Time) {
	for _, profile := range profiles {
		template, ok := preparations[profile.ID]
		// A strict fixed-size preparation is a baseline, never an autonomous candidate.
		if !ok || template.Config.Policy.FixedBatchSize > 0 {
			continue
		}
		instance := template.Instance
		instance.Profile = profileByID[profile.ID]
		instance.ObservedAt = now
		var compatible []string
		for _, d := range s.Workloads {
			if serving.MatchesProfile(d, instance.Profile) {
				compatible = append(compatible, d.UID)
			}
		}
		if len(compatible) > 0 {
			s.Options = append(s.Options, serving.SchedulerOption{Instance: instance, CompatibleUIDs: compatible})
		}
	}
}

func capturePathFeedback(s *serving.SchedulerSnapshot, pathSamples []serving.PathTimingSample, completionMarginMS float64) {
	samples := make(map[string][]serving.PathTimingSample)
	for _, sample := range pathSamples {
		samples[sample.WorkloadUID] = append(samples[sample.WorkloadUID], sample)
	}
	s.PathFeedback = make([]serving.PathFeedback, 0, len(s.Workloads))
	for _, workload := range s.Workloads {
		s.PathFeedback = append(s.PathFeedback, serving.BuildPathFeedback(
			workload.UID, samples[workload.UID], workload.SLO,
			time.Duration(completionMarginMS*float64(time.Millisecond))))
	}
}

func schedulerRequestCoverageError(gaps []serving.ObservationGap, from, to time.Time) error {
	for _, gap := range gaps {
		if gap.Stream == "requests" && gap.Reason == "StreamUnavailable" && gap.From.Before(to) && gap.To.After(from) {
			return &serving.Error{Code: "IncompleteObservation", Message: "request stream unavailable in scheduler observation window"}
		}
	}
	return nil
}
