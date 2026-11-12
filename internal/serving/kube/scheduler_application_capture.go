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
			timing.Window += time.Since(windowStarted)
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
		timing.RefreshList += refreshed.listNanos
		timing.RefreshNodes += refreshed.nodesNanos
		timing.RefreshPods += refreshed.podsNanos
		timing.RefreshResources += refreshed.resourcesNanos
		timing.RefreshDeviceClasses += refreshed.classesNanos
		timing.RefreshServices += refreshed.servicesNanos
		timing.SourceProbe += refreshed.sourceProbeNanos
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
	current, physicalStates := c.runtime.SchedulerInputsAndInstances(time.Now)
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
	s.At, s.Workloads, s.Outstanding = current.At, current.Workloads, current.Outstanding
	s.Routing, s.RoutingCredits = current.Routing, current.RoutingCredits
	capturePathFeedback(&s, window.PathSamples, config.Settings.CompletionMarginMS)
	capturePreparationOptions(&s, profiles, profileByID, config.Preparations, now)
	bindProfileMeters(&s, config.Meters)
	// Placement inventory excludes terminal Pods. Return proof must instead
	// establish exact serving-Pod absence, including Failed/Succeeded Pods.
	c.mu.Lock()
	returning := false
	for _, run := range c.runs {
		if run.reserved && run.result != nil && run.action.Status != servingstate.NativeOperationRunning && run.action.Status != servingstate.NativeOperationCancel {
			returning = true
			break
		}
	}
	c.mu.Unlock()
	if returning {
		listStarted := time.Now()
		exact, err := c.kube.CoreV1().Pods(c.observer.config.Namespace).List(ctx, meta.ListOptions{})
		if timing != nil {
			elapsed := time.Since(listStarted)
			timing.RefreshList += elapsed
			timing.RefreshPods += elapsed
		}
		if err != nil {
			return s, err
		}
		if err := ctx.Err(); err != nil {
			return s, err
		}
		c.releaseObservedScopes(nodes.Items, exact.Items, physicalStates, now)
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	return s, nil
}

// Function ports do not carry admission policy. All accepted values do.
func schedulerApprovalConfiguration(config SchedulerControllerConfig) SchedulerControllerConfig {
	config.OnDecision, config.OnAction = nil, nil
	config.DispatchOccupancySnapshot, config.MeterSource = nil, nil
	// Occupancy policy is the selected cell/value projection below, not raw
	// cache evidence or unused legacy rows.
	config.DispatchOccupancy = nil
	return config
}

// schedulerApprovalInputs retains admission authority, not advancing observation
// clocks or historical replay demand. A changed input discards the evaluation;
// approval never silently rebases a target onto a different physical plan.
func schedulerApprovalInputs(settings serving.SchedulerSettings, s serving.SchedulerSnapshot) serving.SchedulerSnapshot {
	var occupancy []serving.DispatchOccupancy
	if len(s.Instances) != 0 || len(s.Options) != 0 {
		occupancy = serving.SchedulerSelectedOccupancy(settings, s)
		for i := range occupancy {
			occupancy[i].RuntimeImage, occupancy[i].Source, occupancy[i].Provenance = "", "", nil
		}
	}
	s = serving.SchedulerSnapshot{
		Nodes: slices.Clone(s.Nodes), Instances: slices.Clone(s.Instances),
		Options: slices.Clone(s.Options), Workloads: s.Workloads, Routing: s.Routing,
		Costs: s.Costs, Baselines: s.Baselines,
		DispatchOccupancy: occupancy,
		Actions:           slices.Clone(s.Actions), Reservations: slices.Clone(s.Reservations),
	}
	slices.SortFunc(s.Nodes, func(a, b serving.SchedulerNode) int { return strings.Compare(a.Node.UID, b.Node.UID) })
	for i := range s.Nodes {
		s.Nodes[i].ObservedAt = time.Time{}
	}
	for i := range s.Instances {
		s.Instances[i] = s.Instances[i].ApprovalInputs()
	}
	for i := range s.Options {
		s.Options[i].Instance = s.Options[i].Instance.ApprovalInputs()
	}
	for i := range s.Actions {
		s.Actions[i].Elapsed = 0
	}
	for i := range s.Reservations {
		r := &s.Reservations[i]
		r.Nodes = slices.Clone(r.Nodes)
		slices.SortFunc(r.Nodes, func(a, b serving.NodeReference) int { return strings.Compare(a.UID, b.UID) })
		r.WorkloadUIDs, r.PodUIDs, r.Instances = slices.Clone(r.WorkloadUIDs), slices.Clone(r.PodUIDs), slices.Clone(r.Instances)
		slices.Sort(r.WorkloadUIDs)
		slices.Sort(r.PodUIDs)
		slices.Sort(r.Instances)
	}
	return s
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
