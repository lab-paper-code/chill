package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
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

// preparations builds one PrepareSpec per profile. A profile that cannot be
// prepared becomes its own CatalogIssue; the other profiles remain usable.
func (o *SchedulerService) preparations(
	ctx context.Context, config SchedulerConfiguration, profiles []serving.Profile,
) (map[string]servingstate.PrepareSpec, []servingkube.CatalogIssue) {
	preparations := make(map[string]servingstate.PrepareSpec, len(profiles))
	var issues []servingkube.CatalogIssue
	for _, profile := range profiles {
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
	return preparations, issues
}

func (o *SchedulerService) preparation(
	ctx context.Context, config SchedulerConfiguration, profile serving.Profile,
) (servingstate.PrepareSpec, error) {
	if profile.RuntimeMetadata == nil {
		return servingstate.PrepareSpec{}, errors.New("verified physical allocation unavailable")
	}
	spec, err := o.measuredPreparation(ctx, profile, config.Settings.ServingMemoryBytes)
	if err != nil {
		return spec, err
	}
	raw, err := json.Marshal(profile.NativeBundle())
	if err != nil {
		return spec, err
	}
	if int64(len(raw)) > o.opts.MaxEvidenceBytes {
		return spec, errors.New("verified profile bundle exceeds immutable object bound")
	}
	spec.Instance.Profile = &profile
	spec.Bundle = evidencestore.ObjectIdentity{
		Store: o.opts.StoreName, Key: "serving/profiles/" + profile.ID + "/bundle.json", Size: int64(len(raw)),
	}
	if err = servingops.ValidatePreparation(spec); err != nil {
		return spec, err
	}
	_, err = o.objects.PutImmutable(ctx, spec.Bundle, bytes.NewReader(raw))
	return spec, err
}

// Retain both Accepted and Rejected comparisons before publishing the catalog.
// Content-addressed records preserve earlier decisions when annotations change.
func (o *SchedulerService) retainProfileReuseChecks(ctx context.Context, view *servingkube.CatalogView) error {
	for index, check := range view.ReuseChecks {
		check.Record = nil
		raw, err := json.Marshal(struct {
			SchemaVersion string                    `json:"schemaVersion"`
			Check         serving.ProfileReuseCheck `json:"check"`
		}{"chill.dacs.io/profile-reuse-check.v1alpha1", check})
		if err != nil {
			return err
		}
		if int64(len(raw)) > o.opts.MaxEvidenceBytes {
			return errors.New("profile reuse check exceeds immutable object bound")
		}
		reference := evidencestore.ObjectIdentity{Store: o.opts.StoreName,
			Key:  fmt.Sprintf("serving/profile-reuse/%s/%x.json", check.ConfirmationProfileID, sha256.Sum256(raw)),
			Size: int64(len(raw))}
		if _, err := o.objects.PutImmutable(ctx, reference, bytes.NewReader(raw)); err != nil {
			return err
		}
		view.ReuseChecks[index].Record = &reference
		for profileIndex := range view.Profiles {
			profile := &view.Profiles[profileIndex]
			if profile.Reuse != nil && profile.Reuse.ConfirmationProfileID == check.ConfirmationProfileID {
				profile.Reuse.Record = &reference
			}
		}
	}
	return nil
}

// reproducibleRuntimeAccess rejects measured delivery the generated Pod cannot
// reproduce. RKNNLite access comes from the frozen delivery at preparation.
func reproducibleRuntimeAccess(p serving.Profile) error {
	if p.Condition.Backend == "rknnlite" {
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
	if p.RuntimeMetadata == nil || p.Condition.Firstfloor == nil {
		return spec, fmt.Errorf("verified native runtime/settings unavailable")
	}
	runtime := p.RuntimeMetadata
	// RKNNLite runs no ONNX Runtime threads.
	rknn := p.Condition.Backend == "rknnlite"
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
	var memory uint64
	for _, batch := range p.Batches {
		if batch.MemoryBytes == 0 {
			return spec, fmt.Errorf("profile batch %d lacks measured memory", batch.Batch)
		}
		memory = max(memory, batch.MemoryBytes)
	}
	// ponytail: Go's default GOGC=100 lets the firstfloor heap reach twice its live
	// queue, so the queue cap is half the headroom; measure queue RSS if it OOMs again.
	queueBytes := min(servingMemory/2, native.MaxFrameBytes)
	if queueBytes < 1 {
		return spec, fmt.Errorf("settings.servingMemoryBytes must hold the firstfloor queue")
	}
	if memory == 0 || servingMemory > uint64(1<<63-1) || memory > uint64(1<<63-1)-servingMemory {
		return spec, fmt.Errorf("bounded measured memory required")
	}
	memory += servingMemory
	binding := storekube.Binding{
		Store: o.opts.StoreName, Endpoint: o.opts.StoreEndpoint, Bucket: o.opts.StoreBucket,
		Region: o.opts.StoreRegion, CredentialsSecretName: o.opts.StoreCredentialsSecret,
	}
	if err = binding.Validate(); err != nil {
		return spec, err
	}
	hash := sha256.Sum256([]byte(p.ID))
	name := "measured-" + hex.EncodeToString(hash[:12])
	settings := p.Condition.Firstfloor
	if p.Condition.Backend != "ort-cpu" && p.Condition.Backend != "ort-cuda" && !rknn {
		return spec, fmt.Errorf(
			"preparation override required for measured backend %s host delivery contract", p.Condition.Backend,
		)
	}
	spec = servingstate.PrepareSpec{
		Name: name, Store: binding, MemoryBytes: memory,
		Instance: serving.SchedulerInstance{
			ID: name, Node: serving.NodeReference{Name: p.NodeName, UID: p.Condition.NodeUID},
			Allocation: serving.SchedulerAllocation{
				GPUUUID: runtime.GPUUUID, MemoryBytes: memory,
			},
			Capacity: int(settings.MaxQueueSize), Weight: 1,
			MaxBatch: p.Batches[len(p.Batches)-1].Batch,
		},
		Config: native.Config{
			Model:        "chill_model",
			Inputs:       slices.Clone(p.Condition.Preprocessor.Inputs),
			Outputs:      slices.Clone(p.Condition.Preprocessor.Outputs),
			MaxNewTokens: p.Condition.Preprocessor.MaxNewTokens,
			RunnerCommand: []string{
				"python3", "/opt/chill/runtime/firstfloor/runner.py", "--runtime", p.Condition.Backend,
				"--model-dir", "/model", "--preprocessor", "/preprocessor",
				"--warmup-iterations", strconv.Itoa(runtime.WarmupIterations),
			},
			RunnerTimeoutMS: int(
				o.opts.OperationTimeout / time.Millisecond,
			), MaxQueueRequests: int(settings.MaxQueueSize),
			MaxQueueBytes:   int(queueBytes),
			MaxRequestBytes: int(min(o.opts.MaxRequestBytes, int64(native.MaxFrameBytes))),
			Mode:            "serving", BatchSizes: slices.Clone(runtime.InitializedBatches),
			MaxBatchSize: int(settings.MaxBatchSize), ProfilePath: "/profile/bundle.json",
			Policy: native.Policy{
				DecisionID: name, ProfileID: p.ID, MaxBatchSize: p.Batches[len(p.Batches)-1].Batch,
				DeadlinePolicy: settings.DeadlinePolicy,
			},
		},
	}
	// The policy can grow within this immutable measured domain without replacing
	// the runner. Warm all measured K, never additional initialized capabilities.
	for _, batch := range p.Batches {
		spec.Config.RunnerCommand = append(spec.Config.RunnerCommand, "--warmup-batch", strconv.Itoa(batch.Batch))
	}
	if runtime.GPUUUID != "" {
		spec.Config.RunnerCommand = append(spec.Config.RunnerCommand, "--gpu-uuid", runtime.GPUUUID)
	}
	if rknn {
		spec.Config.RunnerCommand = append(spec.Config.RunnerCommand, "--delivery-json", "/config/delivery.json")
	}
	// Validate against the same measured bundle the scheduler publishes, then retain
	// only the immutable startup reference in the persisted preparation.
	checked := spec.Config
	checked.Policy.Batches = p.NativeBundle().Batches
	if err = native.ValidateConfig(&checked); err != nil {
		return servingstate.PrepareSpec{}, err
	}
	return spec, nil
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
