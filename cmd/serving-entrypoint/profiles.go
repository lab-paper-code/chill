package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

const profileConfigurationRequired = "ConfigurationRequired"

func (o *schedulerOwner) allowedProfileNamespace(namespace string) bool {
	if len(o.opts.profileNamespaces) == 0 {
		return namespace == o.opts.servingNamespace
	}
	return slices.Contains(o.opts.profileNamespaces, namespace)
}
func (o *schedulerOwner) readSelectedProfiles(
	ctx context.Context, selections []entrypoint.ProfileCampaign,
) (servingkube.CatalogView, error) {
	keys := make([]types.NamespacedName, 0, len(selections))
	view := servingkube.CatalogView{Profiles: []serving.Profile{}, Insufficiencies: []servingkube.CatalogIssue{}}
	for _, selection := range selections {
		key := types.NamespacedName{Namespace: selection.Namespace, Name: selection.Name}
		if !o.allowedProfileNamespace(selection.Namespace) {
			view.Insufficiencies = append(view.Insufficiencies, servingkube.CatalogIssue{
				Source: key, Code: "ProfileNamespaceNotAllowed",
				Reason: "measurement namespace is outside --profile-namespace allowlist",
			})
			continue
		}
		keys = append(keys, key)
	}
	loaded, err := o.catalog.Refresh(ctx, keys)
	if err != nil {
		return view, err
	}
	view.Profiles = loaded.Profiles
	view.Insufficiencies = append(view.Insufficiencies, loaded.Insufficiencies...)
	return view, nil
}
func profileSelection(
	selection entrypoint.ProfileCampaign, view servingkube.CatalogView,
) entrypoint.ProfileSelectionResult {
	result := entrypoint.ProfileSelectionResult{
		Campaign: selection, Status: "Rejected", Code: "ProfileUnavailable",
		Reason: "measurement contributes no eligible profile in the selected catalog",
	}
	prefix := selection.Namespace + "/" + selection.Name + "/"
	for _, p := range view.Profiles {
		if strings.HasPrefix(p.ID, prefix) {
			result.Status = "Persisted"
			result.ProfileID = p.ID
			result.Code = ""
			result.Reason = ""
			return result
		}
	}
	for _, issue := range view.Insufficiencies {
		if issue.Source.Namespace == selection.Namespace && issue.Source.Name == selection.Name {
			result.Code = issue.Code
			result.Reason = issue.Reason
			if issue.Code == "MeasurementPending" {
				result.Status = "Pending"
			}
			return result
		}
	}
	return result
}
func (o *schedulerOwner) profileSelections(ctx context.Context) (entrypoint.ProfileSelectionsView, error) {
	o.lock.Lock()
	defer o.lock.Unlock()
	result := entrypoint.ProfileSelectionsView{
		Selections: []entrypoint.ProfileSelectionResult{}, State: o.controller.State(),
	}
	cm, err := o.kube.CoreV1().ConfigMaps(o.opts.servingNamespace).Get(ctx, o.opts.schedulerConfigMap, meta.GetOptions{})
	if err != nil {
		return result, err
	}
	config, err := decodeSchedulerConfiguration(cm.Data[o.opts.schedulerConfigKey], o.opts.maxRequestBytes)
	if err != nil {
		return result, err
	}
	result.ConfigVersion = cm.ResourceVersion
	view, err := o.readSelectedProfiles(ctx, config.ProfileCampaigns)
	if err != nil {
		return result, err
	}
	for _, selection := range config.ProfileCampaigns {
		item := profileSelection(selection, view)
		item.Persisted = true
		item.ConfigVersion = cm.ResourceVersion
		active := o.configuration.Load()
		_, prepared := config.Preparations[item.ProfileID]
		item.Loaded = o.profileLoadError == "" && active != nil && prepared &&
			o.resourceVersion == cm.ResourceVersion && item.ProfileID != "" &&
			strings.Contains("\x00"+o.profiles+"\x00", "\x00"+item.ProfileID+"\x00")
		if !item.Loaded && item.Code == "" {
			item.Code = "ProfileLoadPending"
			item.Reason = "selection is persisted; scheduler has not loaded this configuration"
			if !prepared {
				item.Code = profileConfigurationRequired
				item.Reason = "selected profile lacks a preparation; adopt the measurement to generate it"
			}
			if o.profileLoadError != "" {
				item.Code = "ProfileLoadFailed"
				item.Reason = o.profileLoadError
			}
		}
		result.Selections = append(result.Selections, item)
	}
	return result, nil
}
func (o *schedulerOwner) selectProfile(
	ctx context.Context, request entrypoint.ProfileSelectionRequest, principal servingkube.Principal,
) (entrypoint.ProfileSelectionResult, error) {
	selection := request.Campaign
	result := entrypoint.ProfileSelectionResult{Campaign: selection, Status: "Rejected"}
	if len(validation.IsDNS1123Label(selection.Namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(selection.Name)) != 0 {
		return result, &serving.Error{
			Code: "InvalidBody", Message: "valid campaign namespace and name are required", Field: "campaign",
		}
	}
	if !o.allowedProfileNamespace(selection.Namespace) {
		result.Code = "ProfileNamespaceNotAllowed"
		result.Reason = "measurement namespace is outside --profile-namespace allowlist"
		return result, nil
	}
	o.lock.Lock()
	defer o.lock.Unlock()
	cm, err := o.kube.CoreV1().ConfigMaps(o.opts.servingNamespace).Get(ctx, o.opts.schedulerConfigMap, meta.GetOptions{})
	if err != nil {
		return result, err
	}
	config, err := decodeSchedulerConfiguration(cm.Data[o.opts.schedulerConfigKey], o.opts.maxRequestBytes)
	if err != nil {
		return result, err
	}
	result.ConfigVersion = cm.ResourceVersion
	// Verify the requested measurement alone first, so a newer selected condition
	// cannot disguise pending, failed or ineligible input.
	view, err := o.readSelectedProfiles(ctx, []entrypoint.ProfileCampaign{selection})
	if err != nil {
		return result, err
	}
	result = profileSelection(selection, view)
	result.ConfigVersion = cm.ResourceVersion
	result.Persisted = slices.Contains(config.ProfileCampaigns, selection)
	if result.ProfileID == "" {
		return result, nil
	}
	var profile serving.Profile
	for _, p := range view.Profiles {
		if p.ID == result.ProfileID {
			profile = p
			break
		}
	}
	if err = o.checkProfileInventory(config, profile); err != nil {
		result.Status = "Rejected"
		result.Code = profileConfigurationRequired
		result.Reason = err.Error()
		return result, nil
	}
	preparation, exists := config.Preparations[profile.ID]
	if !exists {
		preparation, err = o.measuredPreparation(ctx, profile)
		if err != nil {
			result.Status = "Rejected"
			result.Code = profileConfigurationRequired
			result.Reason = err.Error()
			return result, nil
		}
	}
	// Merge only owned fields into the fetched version, preserving every other
	// operator setting and letting Kubernetes reject a concurrent writer.
	var raw map[string]json.RawMessage
	if err = json.Unmarshal([]byte(cm.Data[o.opts.schedulerConfigKey]), &raw); err != nil {
		return result, err
	}
	changed := !slices.Contains(config.ProfileCampaigns, selection) || !exists
	if changed {
		if !slices.Contains(config.ProfileCampaigns, selection) {
			config.ProfileCampaigns = append(config.ProfileCampaigns, selection)
		}
		if config.Preparations == nil {
			config.Preparations = map[string]servingkube.PrepareSpec{}
		}
		config.Preparations[profile.ID] = preparation
		raw["profileCampaigns"], err = json.Marshal(config.ProfileCampaigns)
		if err != nil {
			return result, err
		}
		raw["preparations"], err = json.Marshal(config.Preparations)
		if err != nil {
			return result, err
		}
		encoded, marshalErr := json.Marshal(raw)
		if marshalErr != nil {
			return result, marshalErr
		}
		if int64(len(encoded)) > o.opts.maxRequestBytes {
			return result, &serving.Error{Code: "InvalidBody", Message: "scheduler configuration exceeds body bound"}
		}
		cm.Data[o.opts.schedulerConfigKey] = string(encoded)
	}
	username, uid := principal.Actor()
	expected, _ := json.Marshal(struct {
		Campaign      entrypoint.ProfileCampaign `json:"campaign"`
		ProfileID     string                     `json:"profileID"`
		ConfigVersion string                     `json:"configVersion"`
		ActorUsername string                     `json:"actorUsername"`
		ActorUID      string                     `json:"actorUID"`
	}{selection, profile.ID, cm.ResourceVersion, username, uid})
	audit := serving.SchedulerActionObservation{
		ID:   "profile-selection/" + profile.ID + "/" + cm.ResourceVersion,
		Kind: "profile-selection", Phase: "validated", StartedAt: time.Now().UTC(), Expected: expected,
	}
	if err = o.journal.SchedulerRecord(serving.SchedulerObservation{At: time.Now().UTC(), Action: &audit}); err != nil {
		return result, err
	}
	if changed {
		cm, err = o.kube.CoreV1().ConfigMaps(o.opts.servingNamespace).Update(ctx, cm, meta.UpdateOptions{})
	}
	if err != nil {
		audit.Phase = "failed"
		audit.Error = err.Error()
	} else {
		result.Persisted = true
		result.Status = "Persisted"
		result.ConfigVersion = cm.ResourceVersion
		result.Loaded = o.profileLoadError == "" && o.resourceVersion == cm.ResourceVersion &&
			strings.Contains("\x00"+o.profiles+"\x00", "\x00"+profile.ID+"\x00")
		audit.Phase = "persisted"
		audit.Actual, _ = json.Marshal(result)
	}
	now := time.Now().UTC()
	audit.CompletedAt = &now
	recordErr := o.journal.SchedulerRecord(serving.SchedulerObservation{At: now, Action: &audit})
	if err != nil {
		return result, err
	}
	return result, recordErr
}
func (o *schedulerOwner) checkProfileInventory(config schedulerConfiguration, p serving.Profile) error {
	if p.RuntimeMetadata == nil {
		return fmt.Errorf("verified physical allocation unavailable")
	}
	allocation := p.RuntimeMetadata
	for _, cpu := range allocation.CPUAffinity {
		if !slices.Contains(config.CPUInventory[p.NodeName], cpu) {
			return fmt.Errorf("cpuInventory[%s] must allow measured CPU %d", p.NodeName, cpu)
		}
		if slices.Contains(config.Reserved[p.NodeName].CPUs, cpu) {
			return fmt.Errorf("measured CPU %d on %s is reserved", cpu, p.NodeName)
		}
	}
	if allocation.GPUUUID != "" {
		if !slices.Contains(config.GPUInventory[p.NodeName], allocation.GPUUUID) {
			return fmt.Errorf("gpuInventory[%s] must allow measured GPU %s", p.NodeName, allocation.GPUUUID)
		}
		if config.Reserved[p.NodeName].GPUUUID == allocation.GPUUUID {
			return fmt.Errorf("measured GPU %s on %s is reserved", allocation.GPUUUID, p.NodeName)
		}
	}
	return nil
}
func (o *schedulerOwner) measuredPreparation(ctx context.Context, p serving.Profile) (servingkube.PrepareSpec, error) {
	var spec servingkube.PrepareSpec
	if p.RuntimeMetadata == nil || p.Condition.Firstfloor == nil {
		return spec, fmt.Errorf("verified native runtime/settings unavailable")
	}
	runtime := p.RuntimeMetadata
	if access := p.Condition.RuntimeAccess; access != nil {
		if access.Privileged || len(access.SupplementalGroups) != 0 {
			return spec, fmt.Errorf(
				"serving preparation cannot reproduce measured runtimeAccess privileged=%t supplementalGroups=%v",
				access.Privileged, access.SupplementalGroups,
			)
		}
		for resource, quantity := range access.Resources {
			if resource != "nvidia.com/gpu" || quantity != "1" || runtime.GPUUUID == "" {
				return spec, fmt.Errorf("serving preparation cannot reproduce measured runtimeAccess resource %s=%s",
					resource, quantity)
			}
		}
	}
	node, err := o.kube.CoreV1().Nodes().Get(ctx, p.NodeName, meta.GetOptions{})
	if err != nil {
		return spec, err
	}
	pool := node.Labels[servingkube.ServingPoolLabel]
	if string(node.UID) != p.Condition.NodeUID || pool == "" {
		return spec, fmt.Errorf("measured Node UID and configured serving pool required")
	}
	var memory uint64
	for _, batch := range p.Batches {
		if batch.MemoryBytes == 0 {
			return spec, fmt.Errorf("profile batch %d lacks measured memory", batch.Batch)
		}
		memory = max(memory, batch.MemoryBytes)
	}
	if memory == 0 || memory > uint64(1<<63-1) || len(runtime.CPUAffinity) == 0 ||
		runtime.IntraOpThreads < 1 || len(runtime.CPUAffinity) < runtime.IntraOpThreads {
		return spec, fmt.Errorf("bounded measured memory and physical CPU/thread allocation required")
	}
	binding := storekube.Binding{
		Store: o.opts.storeName, Endpoint: o.opts.storeEndpoint, Bucket: o.opts.storeBucket,
		Region: o.opts.storeRegion, CredentialsSecretName: o.opts.storeCredentialsSecret,
	}
	if err = binding.Validate(); err != nil {
		return spec, err
	}
	hash := sha256.Sum256([]byte(p.ID))
	name := "measured-" + hex.EncodeToString(hash[:12])
	unknown := "completion-evidence-unavailable"
	settings := p.Condition.Firstfloor
	if p.Condition.Backend != "ort-cpu" && p.Condition.Backend != "ort-cuda" {
		return spec, fmt.Errorf(
			"preparation override required for measured backend %s host delivery contract", p.Condition.Backend,
		)
	}
	spec = servingkube.PrepareSpec{
		Name: name, Store: binding, MemoryBytes: memory,
		Instance: serving.SchedulerInstance{
			ID: name, Node: serving.NodeReference{Name: p.NodeName, UID: p.Condition.NodeUID}, Pool: pool,
			Allocation: serving.SchedulerAllocation{
				CPUs: slices.Clone(runtime.CPUAffinity), CPUCount: len(runtime.CPUAffinity),
				Threads: runtime.IntraOpThreads, GPUUUID: runtime.GPUUUID, MemoryBytes: memory,
			},
			Capacity: int(settings.MaxQueueSize), Weight: 1,
			MaxBatch: p.Batches[len(p.Batches)-1].Batch, EnvelopeSource: unknown,
		},
		Config: native.Config{
			Model: "chill_model", Inputs: runtime.Inputs,
			RunnerCommand: []string{
				"python3", "/opt/chill/runtime/firstfloor/runner.py", "--runtime", p.Condition.Backend,
				"--model", path.Join("/model", "model"+path.Ext(p.Condition.Artifact.Path)),
				"--intra-op-threads", strconv.Itoa(runtime.IntraOpThreads),
				"--warmup-iterations", strconv.Itoa(runtime.WarmupIterations),
			},
			RunnerTimeoutMS: int(o.opts.requestTimeout / time.Millisecond), MaxQueueRequests: int(settings.MaxQueueSize),
			MaxQueueBytes:   int(min(o.opts.maxEvidenceBytes, int64(native.MaxFrameBytes))),
			MaxRequestBytes: int(min(o.opts.maxRequestBytes, int64(native.MaxFrameBytes))),
			Mode:            "serving", BatchSizes: slices.Clone(runtime.InitializedBatches),
			MaxBatchSize: int(settings.MaxBatchSize), ProfilePath: "/profile/bundle.json",
			Policy: native.Policy{
				DecisionID: name, ProfileID: p.ID, MaxBatchSize: p.Batches[len(p.Batches)-1].Batch,
				DeadlinePolicy: settings.DeadlinePolicy, EnvelopeSource: unknown,
			},
		},
	}
	if runtime.GPUUUID != "" {
		spec.Config.RunnerCommand = append(spec.Config.RunnerCommand, "--gpu-uuid", runtime.GPUUUID)
	}
	// Validate against the same measured bundle the owner publishes, then retain
	// only the immutable startup reference in the persisted preparation.
	checked := spec.Config
	checked.Policy.Batches = measuredProfileBundle(p).Batches
	if err = native.ValidateConfig(&checked); err != nil {
		return servingkube.PrepareSpec{}, err
	}
	return spec, nil
}

func measuredProfileBundle(profile serving.Profile) native.ProfileBundle {
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
	return bundle
}
