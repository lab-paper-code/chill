package measurement

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func occupancyInfeasible(reason string) error {
	return &serving.Error{Code: "OccupancyInfeasible", Message: reason}
}

func occupancyPolicyUnavailable(reason string) error {
	return &serving.Error{Code: "OccupancyPolicyUnavailable", Message: reason}
}

// BuildOccupancy reads one fresh configuration and the scheduler's accepted profile
// under its mutation lock. A broken/missing policy never reuses an earlier policy.
func (o *Service) BuildOccupancy(
	ctx context.Context,
	namespace string,
	intent OccupancyIntent,
) (*api.DispatchOccupancyMeasurement, error) {
	if err := o.Namespace(namespace); err != nil {
		return nil, err
	}
	if o.scheduler == nil {
		return nil, occupancyPolicyUnavailable("scheduler configuration is unavailable")
	}
	s := o.scheduler
	s.Lock.Lock()
	defer s.Lock.Unlock()
	cm, err := o.kube.CoreV1().
		ConfigMaps(o.opts.ServingNamespace).
		Get(ctx, o.opts.SchedulerConfigMap, meta.GetOptions{})
	if err != nil {
		return nil, occupancyPolicyUnavailable("scheduler ConfigMap is unavailable")
	}
	policy, err := s.DecodePolicy(cm.Data[o.opts.SchedulerConfigKey], o.opts.MaxRequestBytes)
	if err != nil {
		return nil, occupancyPolicyUnavailable(err.Error())
	}
	if policy == nil {
		return nil, occupancyPolicyUnavailable("dispatchOccupancy block is required")
	}
	configVersion, profiles, preparations, published := s.LoadProfiles()
	if !published || configVersion != cm.ResourceVersion {
		return nil, occupancyPolicyUnavailable("scheduler has not accepted this configuration revision")
	}
	beo := &api.BatchEnergyOptimization{}
	beoKey := client.ObjectKey{Namespace: namespace, Name: intent.BatchEnergyOptimization}
	if err = o.client.Get(ctx, beoKey, beo); err != nil {
		return nil, err
	}
	if beo.Status.Phase != string(api.MeasurementPhaseComplete) || beo.DeletionTimestamp != nil ||
		beo.Spec.State.TargetNode != intent.Node {
		return nil, occupancyInfeasible("BEO must be Complete on the selected Node")
	}
	id := namespace + "/" + beo.Name + "/" + string(beo.UID)
	index := slices.IndexFunc(profiles, func(p serving.Profile) bool {
		return p.ID == id || p.Reuse != nil && p.Reuse.Outcome == "Accepted" && p.Reuse.ConfirmationProfileID == id
	})
	if index < 0 {
		return nil, occupancyInfeasible("BEO has no current accepted profile")
	}
	profile := &profiles[index]
	preparation, ok := preparations[profile.ID]
	if !ok {
		return nil, occupancyInfeasible("selected profile has no accepted native preparation")
	}
	node, err := o.kube.CoreV1().Nodes().Get(ctx, intent.Node, meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	workload := &api.Workload{}
	if err = o.client.Get(ctx, client.ObjectKey{Name: intent.Workload}, workload); err != nil {
		return nil, err
	}
	preprocessor := &api.Preprocessor{}
	if err = o.client.Get(ctx, client.ObjectKey{Name: workload.Spec.PreprocessorRef.Name}, preprocessor); err != nil {
		return nil, err
	}
	return deriveOccupancy(
		namespace,
		intent,
		*policy,
		cm,
		o.opts.SchedulerConfigKey,
		profile,
		node,
		workload,
		preprocessor,
		preparation,
		s.GatewayRequestTimeout,
	)
}

func deriveOccupancy(namespace string, intent OccupancyIntent, policy DispatchOccupancyPolicy,
	cm *core.ConfigMap, key string, profile *serving.Profile, node *core.Node, workload *api.Workload,
	preprocessor *api.Preprocessor, preparation servingstate.PrepareSpec, gatewayTimeout time.Duration,
) (*api.DispatchOccupancyMeasurement, error) {
	requestBytes, err := occupancyTargetInputs(intent, policy, profile, node, workload, preprocessor, gatewayTimeout)
	if err != nil {
		return nil, err
	}
	var selected *serving.BatchProfile
	for index := range profile.Batches {
		if profile.Batches[index].Batch == int(intent.Batch) {
			selected = &profile.Batches[index]
			break
		}
	}
	if selected == nil || len(selected.ServiceSamples) == 0 {
		return nil, occupancyInfeasible("selected batch has no measured service population")
	}
	population := selected.ServiceSamples
	tail, sum := 0.0, 0.0
	for _, sample := range population {
		seconds := float64(sample.ServiceDurationNanos) / 1e9
		if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return nil, occupancyInfeasible("profile batch times must be finite and positive")
		}
		tail, sum = math.Max(tail, seconds), sum+seconds
	}
	mean := sum / float64(len(population))
	if intent.Batch < 1 || math.IsInf(mean, 0) {
		return nil, occupancyInfeasible("invalid batch population")
	}
	// Occupancy and profiling use the same native closed-loop supply bounds.
	queueBytesCap := min(int64(preparation.Config.MaxQueueBytes), int64(64<<20))
	workers := int64(policy.Workers)
	if err := firstfloor.ValidateClosedLoopSupply(int(intent.Batch), int(policy.Workers), preparation.Config.MaxQueueRequests,
		queueBytesCap, requestBytes, float64(policy.RequestLifetimeSeconds), float64(policy.DeliveryMarginMillis), tail); err != nil {
		return nil, occupancyInfeasible(err.Error())
	}
	outstandingBatches := (workers + int64(intent.Batch) - 1) / int64(intent.Batch)
	q := outstandingBatches - 1
	margin := float64(policy.DeliveryMarginMillis) / 1000
	minimumLifetime := math.Max(math.Ceil(workload.Spec.SLO.TargetLatency.Duration.Seconds()),
		math.Ceil(float64(outstandingBatches)*tail+margin))
	if minimumLifetime > float64(policy.RequestLifetimeSeconds) {
		return nil, occupancyInfeasible(
			"configured request lifetime cannot cover queued profile-tail batches and delivery margin",
		)
	}
	drain := math.Ceil(float64(policy.RequestLifetimeSeconds) + tail + margin)
	extension := int64(policy.RequestLifetimeSeconds) + (int64(policy.DeliveryMarginMillis)+999)/1000
	// Include the generator's post-window supply extension, never pad the elapsed cap.
	elapsed := float64(policy.PreparationSeconds) + float64(policy.JobSetupSeconds) +
		float64(policy.CollectionSeconds) + float64(policy.WarmupSeconds) + float64(policy.BurstSeconds) +
		float64(extension) + drain
	if math.IsInf(elapsed, 0) || elapsed > math.Floor(policy.PlannedElapsedCapSeconds) || elapsed > math.MaxInt32 {
		return nil, occupancyInfeasible(
			"elapsed cap cannot hold preparation, warmup, burst, supply extension, drain and job/collection allowances",
		)
	}
	spec := api.DispatchOccupancyMeasurementSpec{
		Node: intent.Node, BatchEnergyOptimization: intent.BatchEnergyOptimization,
		Workload: intent.Workload, Batch: intent.Batch,
		SupplyMode: "closed-loop", Seed: policy.Seed, MinimumPairs: policy.MinimumPairs, Workers: policy.Workers,
		RequestLifetimeSeconds: policy.RequestLifetimeSeconds, DeliveryMarginMillis: policy.DeliveryMarginMillis,
		PreparationSeconds: policy.PreparationSeconds,
		JobSetupSeconds:    policy.JobSetupSeconds, CollectionSeconds: policy.CollectionSeconds,
		WarmupSeconds: policy.WarmupSeconds, DurationSeconds: policy.BurstSeconds,
		DrainSeconds: int32(drain), PlannedElapsedSeconds: int32(elapsed),
		Conditions: api.DispatchOccupancyConditions{
			ProfileID:      profile.ID,
			WaitingBatches: int32(q),
		},
	}
	inputs := map[string]any{
		"dispatchOccupancy": policy,
		"configuration": map[string]any{
			"namespace":       cm.Namespace,
			"name":            cm.Name,
			"uid":             cm.UID,
			"resourceVersion": cm.ResourceVersion,
			"key":             key,
		},
		"node":         serving.NodeReference{Name: node.Name, UID: string(node.UID)},
		"coefficients": policy,
		"profile": map[string]any{
			"profileID":          profile.ID,
			"runtimeImage":       profile.Condition.RuntimeImage,
			"reference":          profile.Reference,
			"evidence":           profile.Evidence,
			"batch":              intent.Batch,
			"durationNanos":      population,
			"meanBatchSeconds":   mean,
			"maxObservedSeconds": tail,
			"workload": map[string]any{"name": workload.Name, "uid": workload.UID, "generation": workload.Generation,
				"slo": map[string]any{
					"percentile":         workload.Spec.SLO.Percentile,
					"targetLatencyNanos": workload.Spec.SLO.TargetLatency.Duration.Nanoseconds(),
				}},
			"preprocessor": map[string]any{
				"name":       preprocessor.Name,
				"uid":        preprocessor.UID,
				"generation": preprocessor.Generation,
				"input":      preprocessor.Spec.Input,
			},
		},
		"computed": map[string]any{
			"tailBatchSeconds":               tail,
			"minimumRequestLifetimeSeconds":  minimumLifetime,
			"framedDecodedRequestUpperBytes": requestBytes,
			"queueRequestCap":                preparation.Config.MaxQueueRequests,
			"queueBytesCap":                  queueBytesCap,
			"supplyExtensionSeconds":         extension,
		},
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return nil, occupancyInfeasible(err.Error())
	}
	spec.Conditions.InputsJSON = string(raw)
	// ValidateConditions includes validateElapsedBudget: validate the final object once.
	if err := spec.ValidateConditions(); err != nil {
		return nil, occupancyInfeasible(err.Error())
	}
	name := intent.Name
	if name == "" {
		frozen, _ := json.Marshal(spec)
		hash := sha256.Sum256(frozen)
		nodePart := strings.TrimRight(intent.Node[:min(len(intent.Node), 40)], "-.")
		beoPart := strings.TrimRight(
			intent.BatchEnergyOptimization[:min(len(intent.BatchEnergyOptimization), 40)],
			"-.",
		)
		name = fmt.Sprintf("occ-%s-%s-b%d-%x", nodePart, beoPart, intent.Batch, hash[:5])
	}
	return &api.DispatchOccupancyMeasurement{
		TypeMeta:   meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "DispatchOccupancyMeasurement"},
		ObjectMeta: meta.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       spec,
	}, nil
}

// occupancyTargetInputs validates current native identities and the request byte bound.
func occupancyTargetInputs(intent OccupancyIntent, policy DispatchOccupancyPolicy,
	profile *serving.Profile, node *core.Node, workload *api.Workload,
	preprocessor *api.Preprocessor, gatewayTimeout time.Duration,
) (int64, error) {
	if node.UID == "" || string(node.UID) != profile.Condition.NodeUID ||
		profile.NodeName != node.Name || intent.Node != node.Name {
		return 0, occupancyInfeasible("current Node UID differs from the measured profile")
	}
	if workload.UID == "" || workload.Spec.DatasetRef == nil ||
		workload.Spec.ModelRef.Name != profile.Condition.LogicalModel ||
		workload.Spec.PreprocessorRef.Name != profile.Condition.Preprocessor.Name ||
		preprocessor.Name != workload.Spec.PreprocessorRef.Name {
		return 0, occupancyInfeasible("Workload model, preprocessor and dataset must resolve the selected profile")
	}
	if workload.Spec.RequestLifetime != nil {
		gatewayTimeout = min(gatewayTimeout, workload.Spec.RequestLifetime.Duration)
	}
	lifetime := float64(policy.RequestLifetimeSeconds)
	if workload.Spec.SLO.TargetLatency.Duration <= 0 ||
		lifetime < math.Ceil(workload.Spec.SLO.TargetLatency.Duration.Seconds()) ||
		gatewayTimeout <= 0 || time.Duration(policy.RequestLifetimeSeconds)*time.Second > gatewayTimeout {
		return 0, occupancyInfeasible("request lifetime must cover ceil(Workload SLO) " +
			"and not exceed the actual gateway request timeout")
	}
	// firstfloor stores each decoded raw field with a four-byte length prefix.
	var requestBytes int64
	if len(preprocessor.Spec.Input.Fields) == 0 ||
		len(preprocessor.Spec.Input.Fields) != len(profile.Condition.Preprocessor.Inputs) {
		return 0, occupancyInfeasible("current raw input contract differs from the profile")
	}
	for i, field := range preprocessor.Spec.Input.Fields {
		if field.MaxBytes < 1 || field.Name != profile.Condition.Preprocessor.Inputs[i] {
			return 0, occupancyInfeasible("bounded matching raw input fields required")
		}
		requestBytes += int64(field.MaxBytes) + 4
	}
	return requestBytes, nil
}
