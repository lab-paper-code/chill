package profilingrun

import (
	"slices"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// ActorConfig derives startup once from the selected execution recipe. Its
// runner warms every initialized batch size (BuildRunnerCommand).
// Callers add resource envelopes and lifecycle-only queue/timeout/policy fields.
func (c RunConfig) ActorConfig() firstfloor.Config {
	settings := c.State.Firstfloor
	actor := firstfloor.Config{
		Model: c.State.Model, RuntimeImage: c.Images.Runtime,
		Backend: c.State.Backend, PowerMode: c.State.PowerMode,
		Artifact: c.State.Artifact, ArtifactImage: c.Images.Artifact,
		ArtifactFiles:     slices.Clone(c.State.ArtifactFiles),
		Preprocessor:      c.State.Preprocessor.Name,
		PreprocessorImage: c.State.Preprocessor.Image,
		PreprocessorPath:  c.State.Preprocessor.Path,
		Inputs:            slices.Clone(c.State.Preprocessor.Inputs),
		Outputs:           slices.Clone(c.State.Preprocessor.Outputs),
		MaxNewTokens:      c.State.Preprocessor.MaxNewTokens,
		WarmupIterations:  c.Measurement.StartupWarmupIterations,
	}
	if points := c.Measurement.OperatingPoints; points != nil {
		actor.MaxQueueBytes = int(points.QueueBytesCap)
	}
	if len(c.Delivery.RuntimePackages) != 0 {
		actor.RuntimePackages = make(map[string]string, len(c.Delivery.RuntimePackages))
		for _, pkg := range c.Delivery.RuntimePackages {
			actor.RuntimePackages[pkg.Name] = pkg.Version
		}
	}
	if settings != nil {
		actor.MaxBatchSize = int(settings.MaxBatchSize)
		actor.MaxQueueRequests = int(settings.MaxQueueSize)
		actor.Policy.DeadlinePolicy = firstfloor.EffectiveDeadlinePolicy(settings.DeadlinePolicy)
		if capability := c.Delivery.ArtifactCapability; capability != nil && len(capability.DeclaredBatchSizes) != 0 {
			for _, batch := range capability.DeclaredBatchSizes {
				if batch <= settings.MaxBatchSize {
					actor.BatchSizes = append(actor.BatchSizes, int(batch))
				}
			}
		} else {
			actor.BatchSizes = make([]int, actor.MaxBatchSize)
			for i := range actor.BatchSizes {
				actor.BatchSizes[i] = i + 1
			}
		}
	}
	slices.Sort(actor.BatchSizes)
	if access := c.Delivery.RuntimeAccess; access != nil {
		actor.GPUUUID = access.GPUUUID
		for name := range access.Resources {
			actor.AcceleratorResource = name
		}
	}
	if host := c.Delivery.HostContract; host != nil && host.Allocation.ResourceName != "" {
		actor.AcceleratorResource = host.Allocation.ResourceName
	}
	actor.BuildRunnerCommand()
	return actor
}
