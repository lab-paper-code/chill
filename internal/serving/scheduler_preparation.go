package serving

import (
	"slices"
)

const (
	PreparationDeterministic    = "deterministic"
	PreparationTransient        = "transient"
	PreparationUnknownTransient = "unknown→transient"
	PreparationPromoted         = "promoted"
)

type PreparationFailureObservation struct {
	Classification string `json:"classification"`
	ReasonKey      string `json:"reasonKey"`
	Reason         string `json:"reason"`
}

// PreparationFailure is an observed inability to start one placement, not a
// timed retry policy. Cleanup and power changes do not change these inputs.
type PreparationFailure struct {
	ProfileID          string              `json:"profileID"`
	RuntimeImage       string              `json:"runtimeImage,omitempty"`
	RunnerCommand      []string            `json:"runnerCommand,omitempty"`
	Allocation         SchedulerAllocation `json:"allocation"`
	ProfileMemoryBytes uint64              `json:"profileMemoryBytes"`
	PodUID             string              `json:"podUID"`
	PreparationFailureObservation
	RepeatCount    uint64 `json:"repeatCount,omitempty"`
	PreviousPodUID string `json:"previousPodUID,omitempty"`
}

func NewPreparationFailure(i SchedulerInstance, podUID string, observation PreparationFailureObservation) *PreparationFailure {
	if i.Profile == nil {
		return nil
	}
	return &PreparationFailure{ProfileID: i.Profile.ID, RuntimeImage: i.RuntimeImage, Allocation: i.Allocation,
		ProfileMemoryBytes: preparationProfileMemory(i.Profile),
		PodUID:             podUID, PreparationFailureObservation: observation, RunnerCommand: i.RunnerCommand}
}

func preparationProfileMemory(profile *Profile) uint64 {
	var memory uint64
	for _, batch := range profile.Batches {
		memory = max(memory, batch.MemoryBytes)
	}
	return memory
}

// Matches compares only inputs determining startup: Node name/UID (through
// node/instance/action), profile ID, allocation limit/GPU UUID, measured profile
// peak memory, actual image and complete rendered RunnerCommand argv.
// Allocatable and observation/Ready timestamps can drift without changing a
// container's limit or startup. They do not release even a promoted unknown;
// known environmental failures remain retryable and are never promoted.
func (f *PreparationFailure) Matches(node SchedulerNode, i SchedulerInstance) bool {
	return f != nil && i.Profile != nil && i.Node == node.Node && f.ProfileID == i.Profile.ID &&
		f.Allocation == i.Allocation && f.ProfileMemoryBytes == preparationProfileMemory(i.Profile) &&
		f.RuntimeImage == i.RuntimeImage && slices.Equal(f.RunnerCommand, i.RunnerCommand)
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
		f.ProfileMemoryBytes != old.ProfileMemoryBytes ||
		f.RuntimeImage != old.RuntimeImage || !slices.Equal(f.RunnerCommand, old.RunnerCommand) {
		return false
	}
	return f.Classification == old.Classification ||
		f.Classification == PreparationUnknownTransient && old.Classification == PreparationPromoted ||
		f.Classification == PreparationPromoted && old.Classification == PreparationUnknownTransient
}

// Keep only the immediately preceding independent attempt and a repetition
// count: retryable environmental failures must not accumulate an unbounded list.
func ObserveRepeatedPreparationFailure(current *Action, previous Action) {
	if !PreparationFailureReproduces(*current, previous) {
		return
	}
	f, old := current.PreparationFailure, previous.PreparationFailure
	f.RepeatCount, f.PreviousPodUID = old.RepeatCount+1, old.PodUID
	if f.Classification == PreparationUnknownTransient {
		f.Classification = PreparationPromoted
	}
}
