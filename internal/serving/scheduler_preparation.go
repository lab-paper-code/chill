package serving

import (
	"slices"
	"strings"
	"time"
)

const (
	PreparationDeterministic    = "deterministic"
	PreparationTransient        = "transient"
	PreparationUnknownTransient = "unknown→transient"
	PreparationPromoted         = "promoted"
)

type PreparationFailureObservation struct {
	Classification      string    `json:"classification"`
	ReasonKey           string    `json:"reasonKey"`
	Reason              string    `json:"reason"`
	NodeReadyTransition time.Time `json:"nodeReadyTransition,omitempty"`
}

// PreparationFailure is an observed inability to start one placement, not a
// timed retry policy. Cleanup and power changes do not change these inputs.
type PreparationFailure struct {
	ProfileID          string              `json:"profileID"`
	RuntimeImage       string              `json:"runtimeImage,omitempty"`
	WarmupArguments    []string            `json:"warmupArguments,omitempty"`
	Allocation         SchedulerAllocation `json:"allocation"`
	ProfileMemoryBytes uint64              `json:"profileMemoryBytes"`
	NodeMemoryBytes    uint64              `json:"nodeMemoryBytes"`
	PodUID             string              `json:"podUID"`
	PreparationFailureObservation
	PreviousPodUIDs []string `json:"previousPodUIDs,omitempty"`
}

func NewPreparationFailure(i SchedulerInstance, nodeMemory uint64, podUID string, observation PreparationFailureObservation) *PreparationFailure {
	if i.Profile == nil || nodeMemory == 0 {
		return nil
	}
	return &PreparationFailure{ProfileID: i.Profile.ID, RuntimeImage: i.RuntimeImage, Allocation: i.Allocation,
		ProfileMemoryBytes: preparationProfileMemory(i.Profile), NodeMemoryBytes: nodeMemory,
		PodUID: podUID, PreparationFailureObservation: observation, WarmupArguments: i.WarmupArguments}
}

func preparationProfileMemory(profile *Profile) uint64 {
	var memory uint64
	for _, batch := range profile.Batches {
		memory = max(memory, batch.MemoryBytes)
	}
	return memory
}

// Matches compares Node name/UID (through node/instance/action), profile ID,
// allocation bytes/GPU UUID, measured profile peak memory, Node allocatable
// memory, actual execution image and rendered runner warmup arguments.
// NodeReady transitions release unknown/promoted environmental evidence, not
// a proven physical OOM. Time, cleanup and a power round-trip are not new inputs.
func (f *PreparationFailure) Matches(node SchedulerNode, i SchedulerInstance) bool {
	return f != nil && i.Profile != nil && i.Node == node.Node && f.ProfileID == i.Profile.ID &&
		f.Allocation == i.Allocation && f.ProfileMemoryBytes == preparationProfileMemory(i.Profile) &&
		f.NodeMemoryBytes == node.MemoryBytes && f.RuntimeImage == i.RuntimeImage &&
		slices.Equal(f.WarmupArguments, i.WarmupArguments) &&
		(f.Classification == PreparationDeterministic || f.NodeReadyTransition.Equal(node.ReadyTransition))
}

// PreparationFailurePresent retains retry evidence until comparable inputs
// change. An absent compatible Workload is not a change to physical evidence.
func PreparationFailurePresent(a Action, snapshot SchedulerSnapshot) bool {
	if a.PreparationFailure == nil {
		return false
	}
	for _, node := range snapshot.Nodes {
		if node.Node != a.Node {
			if node.Node.Name == a.Node.Name || node.Node.UID == a.Node.UID {
				return false
			}
			continue
		}
		found := false
		for _, option := range snapshot.Options {
			i := option.Instance
			if i.Node == a.Node && i.Profile != nil && i.Profile.ID == a.PreparationFailure.ProfileID {
				found = true
				if a.PreparationFailure.Matches(node, i) {
					return true
				}
			}
		}
		return !found
	}
	return true
}

// PreparationFailureReproduces compares independent attempts, not repeated
// reads of one receipt. Known environmental reasons remain transient forever.
func PreparationFailureReproduces(current, previous Action) bool {
	f, old := current.PreparationFailure, previous.PreparationFailure
	if f == nil || old == nil || current.ID == previous.ID || f.PodUID == old.PodUID ||
		current.Node != previous.Node || f.Classification == PreparationDeterministic ||
		f.ReasonKey != old.ReasonKey || f.ProfileID != old.ProfileID || f.Allocation != old.Allocation ||
		f.ProfileMemoryBytes != old.ProfileMemoryBytes || f.NodeMemoryBytes != old.NodeMemoryBytes ||
		f.RuntimeImage != old.RuntimeImage || !slices.Equal(f.WarmupArguments, old.WarmupArguments) ||
		!f.NodeReadyTransition.Equal(old.NodeReadyTransition) {
		return false
	}
	return f.Classification == old.Classification ||
		f.Classification == PreparationUnknownTransient && old.Classification == PreparationPromoted ||
		f.Classification == PreparationPromoted && old.Classification == PreparationUnknownTransient
}

func ObserveRepeatedPreparationFailure(current *Action, previous Action) {
	if !PreparationFailureReproduces(*current, previous) {
		return
	}
	f, old := current.PreparationFailure, previous.PreparationFailure
	f.PreviousPodUIDs = append(slices.Clone(old.PreviousPodUIDs), old.PodUID)
	if f.Classification == PreparationUnknownTransient {
		f.Classification = PreparationPromoted
	}
}

// The newest fact carries the complete observed UID list. Superseded execution
// receipts can therefore keep their ordinary observation-window lifetime.
func PreparationFailureSupersedes(current, previous Action) bool {
	return PreparationFailureReproduces(current, previous) &&
		slices.Contains(current.PreparationFailure.PreviousPodUIDs, previous.PreparationFailure.PodUID)
}

// RunnerWarmupArguments reads the rendered startup data; it does not infer a
// warmup policy from profiles or depend on the code that constructs the command.
func RunnerWarmupArguments(command []string) []string {
	var arguments []string
	for index := 0; index < len(command); index++ {
		if strings.HasPrefix(command[index], "--warmup-batch=") {
			arguments = append(arguments, command[index])
		} else if command[index] == "--warmup-batch" {
			arguments = append(arguments, command[index])
			if index+1 < len(command) {
				index++
				arguments = append(arguments, command[index])
			}
		}
	}
	return arguments
}
