package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const preparationUnavailable = "PreparationUnavailable"

// preparations builds each available profile independently. A failed source
// withholds only its preparation; cancellation stops the refresh.
func (o *SchedulerService) preparations(
	ctx context.Context, config SchedulerConfiguration, profiles []serving.Profile,
) (map[string]servingstate.PrepareSpec, []servingkube.CatalogIssue, error) {
	preparations := make(map[string]servingstate.PrepareSpec, len(profiles))
	var issues []servingkube.CatalogIssue
	for _, profile := range profiles {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		spec, err := o.preparation(ctx, config, profile)
		if err != nil {
			source := strings.SplitN(profile.ID, "/", 3)
			issues = append(issues, servingkube.CatalogIssue{
				Source: types.NamespacedName{Namespace: source[0], Name: source[1]},
				Code:   preparationUnavailable, Reason: err.Error(),
			})
			continue
		}
		preparations[profile.ID] = spec
	}
	return preparations, issues, ctx.Err()
}

func (o *SchedulerService) preparation(
	ctx context.Context, config SchedulerConfiguration, profile serving.Profile,
) (servingstate.PrepareSpec, error) {
	if profile.Batches[0].Batch != 1 {
		return servingstate.PrepareSpec{}, errors.New("measured batch-1 required")
	}
	spec, err := o.measuredPreparation(ctx, profile, config.Settings.ServingMemoryBytes)
	if err != nil {
		return spec, err
	}
	spec.Instance.RuntimeImage = spec.Config.RuntimeImage
	spec.Instance.RunnerCommand = spec.Config.RunnerCommand
	spec.Instance.Profile = &profile
	if accepted := o.Profiles.Load(); accepted != nil {
		if published, ok := accepted.Preparations[profile.ID]; ok {
			// The native population comes from one immutable session result.
			// A BEO revision may change annotations without changing that result.
			for _, previous := range accepted.Profiles {
				if previous.ID == profile.ID && previous.Reference == profile.Reference {
					spec.Bundle = published.Bundle
					return spec, nil
				}
			}
		}
	}
	raw, err := json.Marshal(profile.NativeBundle())
	if err != nil {
		return spec, err
	}
	if int64(len(raw)) > o.opts.MaxEvidenceBytes {
		return spec, errors.New("verified profile bundle exceeds immutable object bound")
	}
	// Content addressing separates both new evidence and a new bundle wire
	// format from any already stored immutable bundle.
	hash := sha256.Sum256(raw)
	spec.Bundle = evidencestore.ObjectIdentity{
		Store: o.opts.StoreName, Key: "serving/profiles/" + profile.ID + "/" + hex.EncodeToString(hash[:]) + "/bundle.json", Size: int64(len(raw)),
	}
	if _, err = o.objects.PutImmutable(ctx, spec.Bundle, bytes.NewReader(raw)); err != nil {
		return spec, err
	}
	return spec, nil
}

// rknnLiteBackend runs its model on the RKNN NPU from a measured host delivery.
const rknnLiteBackend = "rknnlite"

// reproducibleRuntimeAccess rejects measured delivery the generated Pod cannot
// reproduce. RKNNLite access comes from the frozen delivery at preparation.
func reproducibleRuntimeAccess(p serving.Profile) error {
	if p.Condition.Backend == rknnLiteBackend {
		if len(p.RuntimeDelivery) == 0 {
			return fmt.Errorf("measured RKNNLite delivery unavailable")
		}
		return nil
	}
	access := p.Condition.RuntimeAccess
	if access == nil {
		return nil
	}
	// SupplementalGroups are reproduced on the serving Pod security context.
	if access.Privileged {
		return fmt.Errorf("serving preparation cannot reproduce measured runtimeAccess privileged=true")
	}
	for resource, quantity := range access.Resources {
		if resource != "nvidia.com/gpu" || quantity != "1" || p.RuntimeMetadata.GPUUUID == "" {
			return fmt.Errorf("serving preparation cannot reproduce measured runtimeAccess resource %s=%s",
				resource, quantity)
		}
	}
	return nil
}

// measuredPreparation sizes the Pod memory as the measured peak plus
// servingMemory, the headroom for the requests firstfloor queues.
func (o *SchedulerService) measuredPreparation(
	ctx context.Context, p serving.Profile, servingMemory uint64,
) (servingstate.PrepareSpec, error) {
	var spec servingstate.PrepareSpec
	runtime := p.RuntimeMetadata
	// RKNNLite runs no ONNX Runtime threads.
	rknn := p.Condition.Backend == rknnLiteBackend
	if err := reproducibleRuntimeAccess(p); err != nil {
		return spec, err
	}
	node, err := o.kube.CoreV1().Nodes().Get(ctx, p.NodeName, meta.GetOptions{})
	if err != nil {
		return spec, err
	}
	if string(node.UID) != p.Condition.NodeUID {
		return spec, fmt.Errorf("measured Node UID required")
	}
	memory := p.Condition.Actor.MemoryLimitBytes
	queueBytes, err := measuredQueueBytes(p, servingMemory)
	if err != nil {
		return spec, err
	}
	binding := storekube.Binding{
		Store: o.opts.StoreName, Endpoint: o.opts.StoreEndpoint, Bucket: o.opts.StoreBucket,
		Region: o.opts.StoreRegion, CredentialsSecretName: o.opts.StoreCredentialsSecret,
	}
	hash := sha256.Sum256([]byte(p.ID))
	name := "measured-" + hex.EncodeToString(hash[:12])
	settings := p.Condition.Firstfloor
	actor := p.Condition.Actor
	// Serving initializes and warms exactly the measured batch sizes: a size
	// the session stopped at (a capacity limit) or never reached has no
	// profile and may not fit.
	actor.BatchSizes = make([]int, len(p.Batches))
	for i, batch := range p.Batches {
		actor.BatchSizes[i] = batch.Batch
	}
	actor.BuildRunnerCommand()
	actor.Mode = "serving"
	actor.ProfilePath = "/profile/bundle.json"
	actor.RunnerTimeoutMS = int(o.opts.OperationTimeout / time.Millisecond)
	actor.MaxQueueBytes = int(queueBytes)
	actor.MaxRequestBytes = int(min(o.opts.MaxRequestBytes, int64(native.MaxFrameBytes)))
	actor.Policy = native.Policy{
		DecisionID: name, ProfileID: p.ID, MaxBatchSize: p.Batches[len(p.Batches)-1].Batch,
	}
	spec = servingstate.PrepareSpec{
		Name: name, Store: binding, MemoryBytes: uint64(memory),
		Instance: serving.SchedulerInstance{
			ID: name, Node: serving.NodeReference{Name: p.NodeName, UID: p.Condition.NodeUID},
			Allocation: serving.SchedulerAllocation{
				GPUUUID: runtime.GPUUUID, MemoryBytes: uint64(memory),
			},
			Capacity: int(settings.MaxQueueSize), Weight: 1,
			MaxBatch: p.Batches[len(p.Batches)-1].Batch,
		},
		Config: actor,
	}
	if rknn {
		spec.Delivery.RKNNLite = p.RuntimeDelivery
	} else if access := p.Condition.RuntimeAccess; access != nil {
		spec.Delivery.SupplementalGroups = access.SupplementalGroups
	}
	return spec, nil
}

// measuredQueueBytes is shared by full preparation and policy-only acceptance.
// The measured resource envelope remains fixed when user queue headroom changes.
func measuredQueueBytes(p serving.Profile, servingMemory uint64) (uint64, error) {
	memory := p.Condition.Actor.MemoryLimitBytes
	var peak uint64
	for _, batch := range p.Batches {
		peak = max(peak, batch.MemoryBytes)
	}
	if peak >= uint64(memory) {
		return 0, fmt.Errorf("measured actor memory envelope has no queue headroom")
	}
	// ponytail: Go's default GOGC=100 lets the firstfloor heap reach twice its live
	// queue, so the queue cap is half the headroom; measure queue RSS if it OOMs again.
	queueBytes := min(min(servingMemory, uint64(memory)-peak)/2, native.MaxFrameBytes)
	if queueBytes < 1 {
		return 0, fmt.Errorf("settings.servingMemoryBytes must hold the firstfloor queue")
	}
	return queueBytes, nil
}

// readCatalog projects only query inputs; the scheduler remains the source of truth.
func (o *SchedulerService) ReadCatalog(ctx context.Context) (servingkube.CatalogRead, error) {
	view, err := o.measurementCatalogView(ctx)
	if err != nil {
		return servingkube.CatalogRead{}, err
	}
	read := servingkube.CatalogRead{
		CatalogSnapshot: servingkube.ProjectCatalogSnapshot(view.CatalogView, view.ConfigVersion, nil),
	}
	current := o.Profiles.Load()
	occupancy := o.occupancy.published.Load()
	if occupancy != nil {
		current = occupancy.input.catalog
	}
	if current != nil {
		accepted := servingkube.ProjectCatalogSnapshot(current.CatalogView, current.ConfigVersion, current.Preparations)
		accepted.Occupancy = current.Occupancy
		accepted.OccupancySkipped = current.OccupancySkipped
		if occupancy != nil {
			accepted.DispatchOccupancy = occupancy.selected
			accepted.OccupancyEstimates = occupancy.estimates
		}
		read.Accepted = &accepted
	}
	return read, nil
}
