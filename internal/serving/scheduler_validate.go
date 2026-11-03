package serving

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// schedulerRouted reports whether some instance of c receives uid's requests.
func schedulerRouted(c SchedulerCandidate, uid string) bool {
	for _, i := range c.Instances {
		weight := int32(0)
		if i.WorkloadUID == uid {
			weight = i.Weight
		}
		if route, explicit := c.Routing[uid]; explicit {
			weight = route[i.ID]
		}
		if weight > 0 {
			return true
		}
	}
	return false
}

// SchedulerFixedBatchAutonomy rejects autonomy for a Workload still routed to a
// strict fixed-batch baseline; the operator removes that routing first.
func SchedulerFixedBatchAutonomy(snapshot SchedulerSnapshot, autonomous []string) error {
	fixed := SchedulerCandidate{Routing: snapshot.Routing}
	for _, i := range snapshot.Instances {
		if i.FixedBatchSize > 0 {
			fixed.Instances = append(fixed.Instances, i)
		}
	}
	for _, uid := range autonomous {
		if schedulerRouted(fixed, uid) {
			return fmt.Errorf("workload %s is routed to a fixedBatchSize instance; remove that routing before enabling autonomy", uid)
		}
	}
	return nil
}

func appendUnique(xs []string, more ...string) []string {
	for _, s := range more {
		if !slices.Contains(xs, s) {
			xs = append(xs, s)
		}
	}
	return xs
}

// SchedulerContinuationReasons validates fresh physical evidence without
// evaluating candidates or admitting a new scheduler decision.
func SchedulerContinuationReasons(settings SchedulerSettings, snapshot SchedulerSnapshot) []string {
	reasons := append(slices.Clone(snapshot.Reasons), schedulerValidateSnapshot(settings, snapshot)...)
	return appendUnique(reasons, schedulerValidateResources(settings, snapshot.Nodes, snapshot.Instances)...)
}

func schedulerValidateSnapshot(settings SchedulerSettings, snapshot SchedulerSnapshot) []string {
	reasons := schedulerValidateObservationTimes(&snapshot, settings.MaxObservationAge)
	if len(snapshot.Nodes) == 0 {
		reasons = append(reasons, "MissingPoolInventory")
	}
	ids := map[string]bool{}
	for _, i := range snapshot.Instances {
		ids[i.ID] = true
	}
	for _, r := range snapshot.Outstanding {
		reasons = schedulerValidateRequest(r, ids, reasons)
	}
	for _, a := range snapshot.Arrivals {
		if (a.Compute || a.Accepted) && a.Lifetime < 0 {
			reasons = appendUnique(reasons, "InvalidArrivalLifetime:"+a.ID)
		}
	}
	definitions := append([]*Definition(nil), snapshot.Workloads...)
	for _, r := range snapshot.Outstanding {
		definitions = append(definitions, r.Definition)
	}
	for _, a := range snapshot.Arrivals {
		if a.Compute || a.Accepted {
			definitions = append(definitions, a.Definition)
		}
	}
	for _, d := range definitions {
		if d == nil {
			reasons = appendUnique(reasons, "MissingImmutableDefinition")
			continue
		}
		if d.SLO.Percentile <= 0 || d.SLO.Percentile > 100 || !schedulerFinite(d.SLO.Percentile) || d.SLO.TargetLatency <= 0 {
			reasons = appendUnique(reasons, "InvalidSLO:"+d.UID)
		}
	}
	return reasons
}

func schedulerValidateObservationTimes(snapshot *SchedulerSnapshot, maxAge time.Duration) []string {
	reasons := []string{}
	if snapshot.At.IsZero() || snapshot.ObservationEnd.IsZero() || snapshot.ObservationEnd.After(snapshot.At) {
		reasons = append(reasons, "UnknownOrFutureObservationWindow")
	}
	for _, n := range snapshot.Nodes {
		if n.Node.UID == "" || n.ObservedAt.IsZero() || n.ObservedAt.After(snapshot.At) ||
			snapshot.At.Sub(n.ObservedAt) > maxAge {
			reasons = appendUnique(reasons, "UnknownOrStaleNode:"+n.Node.Name)
		}
	}
	for _, i := range snapshot.Instances {
		if i.Terminated || !i.Ready {
			continue // Non-serving physical occupancy contributes no live capacity.
		}
		if schedulerFixedReason(*snapshot, i.WorkloadUID) != "" || schedulerFixedReason(*snapshot, i.OwnerWorkloadUID) != "" {
			continue
		}
		if i.ObservedAt.IsZero() || i.ObservedAt.After(snapshot.At) || snapshot.At.Sub(i.ObservedAt) > maxAge {
			reasons = appendUnique(reasons, "UnknownOrStaleInstance:"+i.ID)
		}
	}
	return reasons
}

func schedulerValidateRequest(r SchedulerRequest, ids map[string]bool, reasons []string) []string {
	if r.Definition == nil || r.ReceivedAt.IsZero() || r.Elapsed < 0 || r.QueueAge < 0 || r.RunningElapsed < 0 {
		reasons = appendUnique(reasons, "UnknownRequestContext:"+r.ID)
		return reasons
	}
	if r.Phase != phasePending && r.Phase != phaseAssigned && r.Phase != phaseQueued && r.Phase != phaseDeferred && r.Phase != phaseRunning && r.Phase != phaseReturning {
		reasons = appendUnique(reasons, "UnknownRequestPhase:"+r.ID)
	}
	if r.Phase != phasePending && !ids[r.Instance] {
		reasons = appendUnique(reasons, "UnknownNativeInstance:"+r.ID)
	}
	if r.Phase == phaseRunning && r.BatchID == "" {
		reasons = appendUnique(reasons, "UnknownRunningBatch:"+r.ID)
	}
	if r.RemainingLifetime != nil && *r.RemainingLifetime < 0 {
		reasons = appendUnique(reasons, "InvalidRequestLifetime:"+r.ID)
	}
	if r.Phase == phaseReturning && r.ReturnElapsed < 0 {
		reasons = appendUnique(reasons, "MissingReturningCompletionEvidence:"+r.ID)
	}
	if (r.Phase == phaseQueued || r.Phase == phaseDeferred || r.Phase == phaseRunning) && (r.NativeSenderElapsedMS == nil || !schedulerFinite(*r.NativeSenderElapsedMS) || *r.NativeSenderElapsedMS < 0) {
		reasons = appendUnique(reasons, "MissingNativeSenderContext:"+r.ID)
	}
	return reasons
}

// schedulerValidateResources checks the budget of every Node that hosts an
// instance; a Node without instances stays in the POOL with its own reasons.
func schedulerValidateResources(settings SchedulerSettings, nodes []SchedulerNode, instances []SchedulerInstance) []string {
	reasons := []string{}
	seen := map[string]bool{}
	for _, n := range nodes {
		if seen[n.Node.UID] {
			reasons = appendUnique(reasons, "DuplicateNode:"+n.Node.UID)
		}
		seen[n.Node.UID] = true
		allocations := append([]SchedulerAllocation{n.Reserved}, n.NonServing...)
		hosted := false
		for _, i := range instances {
			if i.Node != n.Node {
				continue
			}
			hosted = true
			// Retained occupancy is not an executable candidate. Its missing
			// native evidence fixes this Node, but must not reject other Nodes.
			if i.Terminated || !i.Ready && slices.Contains(n.Reasons, NodeInstanceUnavailable) {
				allocations = append(allocations, i.Allocation)
				continue
			}
			if i.Profile == nil {
				reasons = appendUnique(reasons, "MissingProfile:"+i.ID)
				continue
			}
			reasons = schedulerValidateInstance(settings, n, i, reasons)
			allocations = append(allocations, i.Allocation)
		}
		if hosted {
			reasons = ValidateNodeBudget(n, allocations, reasons)
		}
	}
	for _, i := range instances {
		if !seen[i.Node.UID] {
			reasons = appendUnique(reasons, "UnknownPlacementNode:"+i.ID)
		}
	}
	return reasons
}

func schedulerValidateInstance(settings SchedulerSettings, n SchedulerNode, i SchedulerInstance, reasons []string) []string {
	if !n.Ready {
		reasons = appendUnique(reasons, "NodeUnavailable:"+n.Node.UID)
	}
	if i.Profile.Condition.NodeUID != n.Node.UID || i.Profile.Condition.PowerMode != n.PowerMode {
		reasons = appendUnique(reasons, "ProfileConditionMismatch:"+i.ID)
	}
	if i.Capacity < 1 || i.Weight < 0 || i.MaxWait < 0 {
		reasons = appendUnique(reasons, "MissingQueueOrTransportEvidence:"+i.ID)
	}
	if len(i.Profile.Batches) == 0 || i.Profile.ID == "" || len(i.Profile.Evidence) == 0 {
		reasons = appendUnique(reasons, "MissingProfileEvidence:"+i.ID)
	}
	reasons, maxMemory := schedulerValidateService(i, reasons)
	if maxMemory == 0 || maxMemory > i.Allocation.MemoryBytes || settings.ServingMemoryBytes > i.Allocation.MemoryBytes-maxMemory {
		reasons = appendUnique(reasons, "MeasuredMemoryInfeasible:"+i.ID)
	}
	return reasons
}

func schedulerValidateService(i SchedulerInstance, reasons []string) ([]string, uint64) {
	maxMemory := uint64(0)
	single := false
	for _, b := range i.Profile.Batches {
		if b.Batch > i.MaxBatch {
			continue
		}
		if b.Batch == 1 {
			single = true
		}
		if b.MemoryBytes > maxMemory {
			maxMemory = b.MemoryBytes
		}
		if len(b.DurationNanos) == 0 || b.EnergyPerInputJoules < 0 || !schedulerFinite(b.EnergyPerInputJoules) {
			reasons = appendUnique(reasons, "MissingServiceEnergyEvidence:"+i.ID)
		}
		for _, v := range b.DurationNanos {
			if v <= 0 || !schedulerFinite(v) {
				reasons = appendUnique(reasons, "InvalidServiceSample:"+i.ID)
			}
		}
	}
	if !single {
		reasons = appendUnique(reasons, "MissingSingleBatchEvidence:"+i.ID)
	}
	return reasons, maxMemory
}

// ValidateNodeBudget checks memory safety and exclusive GPU allocation.
func ValidateNodeBudget(n SchedulerNode, allocations []SchedulerAllocation, reasons []string) []string {
	gpu := map[string]bool{}
	var memory uint64
	for _, a := range allocations {
		if math.MaxUint64-memory < a.MemoryBytes {
			reasons = appendUnique(reasons, "MemoryBudgetOverflow:"+n.Node.UID)
		} else {
			memory += a.MemoryBytes
		}
		if a.GPUUUID != "" {
			if !slices.Contains(n.GPUUUIDs, a.GPUUUID) || gpu[a.GPUUUID] {
				reasons = appendUnique(reasons, "ExclusiveGPUInfeasible:"+n.Node.UID)
			}
			gpu[a.GPUUUID] = true
		}
	}
	if memory > n.MemoryBytes {
		reasons = appendUnique(reasons, "MemoryBudgetInfeasible:"+n.Node.UID)
	}
	return reasons
}

func schedulerPolicy(i SchedulerInstance) firstfloor.Policy {
	p := firstfloor.Policy{
		ProfileID: i.Profile.ID, MaxBatchSize: i.MaxBatch, MaxBatchWaitMS: float64(i.MaxWait) / 1e6,
	}
	if i.Profile.Condition.Firstfloor != nil {
		p.DeadlinePolicy = i.Profile.Condition.Firstfloor.DeadlinePolicy
	}
	p.DeadlinePolicy = firstfloor.EffectiveDeadlinePolicy(p.DeadlinePolicy)
	for _, b := range i.Profile.Batches {
		samples := make([]float64, len(b.DurationNanos))
		for j, v := range b.DurationNanos {
			samples[j] = v / 1e6
		}
		sort.Float64s(samples)
		p.Batches = append(p.Batches, firstfloor.BatchProfile{Size: b.Batch, ServiceSamplesMS: samples, EnergyPerItemJ: b.EnergyPerInputJoules})
	}
	return p
}
