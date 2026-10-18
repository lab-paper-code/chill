package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
)

type PowerCostSource struct {
	Measurement types.NamespacedName `json:"measurement"`
	Meter       MeterDeclaration     `json:"meter"`
}

func boundedCostRead(ctx context.Context, reader evidencestore.Reader, ref evidencestore.ObjectIdentity, maxBytes int64) ([]byte, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if ref.Size > maxBytes || maxBytes < 1 {
		return nil, errors.New("cost object exceeds bound")
	}
	stream, err := reader.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(stream, ref.Size+1))
	err = errors.Join(readErr, stream.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != ref.Size {
		return nil, errors.New("cost exact size mismatch")
	}
	return raw, nil
}
func strictCostDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing cost JSON")
	}
	return nil
}
func (o *Operations) LoadPowerCosts(ctx context.Context, objects evidencestore.Reader, sources []PowerCostSource, maxBytes int64) ([]serving.OperationCost, error) {
	var costs []serving.OperationCost
	var failures []error
	for _, source := range sources {
		loaded, err := o.loadPowerCost(ctx, objects, source, maxBytes)
		if err != nil {
			failures = append(failures, fmt.Errorf("power measurement %s: %w", source.Measurement, err))
			continue
		}
		costs = append(costs, loaded...)
	}
	return costs, errors.Join(failures...)
}
func (o *Operations) loadPowerCost(ctx context.Context, objects evidencestore.Reader, source PowerCostSource, maxBytes int64) ([]serving.OperationCost, error) {
	measurement := &edge.PowerModeTransitionCost{}
	if err := o.store.client.Get(ctx, source.Measurement, measurement); err != nil {
		return nil, err
	}
	if measurement.DeletionTimestamp != nil || measurement.UID == "" || measurement.Status.ObservedGeneration != measurement.Generation || measurement.Status.Phase != edge.PowerModeTransitionCostPhaseComplete || measurement.Status.Result == nil {
		return nil, errors.New("current Complete power measurement required")
	}
	preparedWire, err := o.readPowerPreparation(ctx, source.Measurement, measurement.UID)
	if err != nil {
		return nil, err
	}
	prepared := preparedWire.Prepared
	node := serving.NodeReference{Name: prepared.TargetNode.Name, UID: prepared.TargetNode.UID}
	if node != source.Meter.Node || measurement.Spec.TargetNode != node.Name || source.Meter.Scope != "node-exclusive-wall" || source.Meter.ID == "" || !reflect.DeepEqual(prepared.PowerSource, source.Meter.Source) {
		return nil, errors.New("power source exact physical Node/meter mismatch")
	}
	ref := measurement.Status.Result
	identity := evidencestore.ObjectIdentity{Store: ref.Store, Key: ref.Key, Size: ref.Size}
	raw, err := boundedCostRead(ctx, objects, identity, maxBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		SchemaVersion string `json:"schemaVersion"`
		Outcome       string `json:"outcome"`
		Reason        string `json:"reason,omitempty"`
		Directions    []struct {
			AcceptedCount int32                          `json:"acceptedCount"`
			FinalEvidence []evidencestore.ObjectIdentity `json:"finalEvidence"`
		} `json:"directions"`
		DirectedTable *transitioncost.DirectedTableRecord `json:"directedTable,omitempty"`
	}
	if err = strictCostDecode(raw, &result); err != nil {
		return nil, err
	}
	if result.SchemaVersion != transitioncost.MeasurementResultSchemaVersion || result.Outcome != "Complete" || result.DirectedTable == nil || len(result.Directions) != len(prepared.Directions) {
		return nil, errors.New("complete directed result contract missing")
	}
	if _, err = transitioncost.RestoreDirectedTable(*result.DirectedTable); err != nil {
		return nil, err
	}
	costs := make([]serving.OperationCost, 0, len(prepared.Directions))
	for index, direction := range prepared.Directions {
		cost := serving.OperationCost{ID: source.Measurement.String() + "/" + direction.FrozenContract.SourceState.PowerMode + "->" + direction.FrozenContract.TargetState.PowerMode, Node: node, Kind: operationPower, From: direction.FrozenContract.SourceState.PowerMode, To: direction.FrozenContract.TargetState.PowerMode, EnergyScope: "whole-wall", Meter: source.Meter.ID, Scope: source.Meter.Scope, Reference: identity}
		for _, finalRef := range result.Directions[index].FinalEvidence {
			if err := o.appendPowerTrial(ctx, objects, finalRef, maxBytes, direction.FrozenContract, prepared.Policy, source.Meter, &cost); err != nil {
				return nil, err
			}
		}
		if len(cost.Trials) != int(result.Directions[index].AcceptedCount) || len(cost.Trials) < int(measurement.Spec.AcceptedCountTarget) {
			return nil, errors.New("accepted directed raw trial count incomplete")
		}
		costs = append(costs, cost)
	}
	return costs, nil
}
func (o *Operations) readPowerPreparation(ctx context.Context, key types.NamespacedName, owner types.UID) (powerPreparationWire, error) {
	var preparedWire powerPreparationWire
	preparation := &core.ConfigMap{}
	if err := o.store.client.Get(ctx, key, preparation); err != nil {
		return preparedWire, err
	}
	if preparation.Immutable == nil || !*preparation.Immutable || len(preparation.OwnerReferences) != 1 || preparation.OwnerReferences[0].UID != owner || preparation.OwnerReferences[0].Kind != "PowerModeTransitionCost" {
		return preparedWire, errors.New("immutable owned power preparation required")
	}
	if err := strictCostDecode(preparation.BinaryData["preparation.json"], &preparedWire); err != nil {
		return preparedWire, err
	}
	if preparedWire.SchemaVersion != transitioncost.PreparationRecordSchemaVersion || preparedWire.Prepared == nil {
		return preparedWire, errors.New("unsupported power preparation schema")
	}
	return preparedWire, nil
}
func (o *Operations) appendPowerTrial(ctx context.Context, objects evidencestore.Reader, finalRef evidencestore.ObjectIdentity, maxBytes int64, contract transitioncost.FrozenContract, policy transitioncost.Policy, meter MeterDeclaration, cost *serving.OperationCost) error {
	payload, err := boundedCostRead(ctx, objects, finalRef, maxBytes)
	if err != nil {
		return err
	}
	var final struct {
		SchemaVersion string                                    `json:"schemaVersion"`
		RawEvidence   evidencestore.ObjectIdentity              `json:"rawEvidence"`
		Observation   transitioncost.FinalizedObservationRecord `json:"observation"`
	}
	if err = strictCostDecode(payload, &final); err != nil {
		return err
	}
	if final.SchemaVersion != transitioncost.FinalizedRecordSchemaVersion || !reflect.DeepEqual(final.Observation.FrozenContract, contract) {
		return errors.New("directed trial frozen conditions differ")
	}
	if _, err = transitioncost.RestoreFinalizedObservation(final.Observation); err != nil {
		return err
	}
	rawTrial, err := boundedCostRead(ctx, objects, final.RawEvidence, maxBytes)
	if err != nil {
		return err
	}
	observation, err := transitioncost.DecodeRawRecord(rawTrial)
	if err != nil {
		return err
	}
	derived, err := transitioncost.Derive(contract, observation, policy)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(derived.Record(), final.Observation) {
		return errors.New("retained raw directed trial differs from stored accepted projection")
	}
	accepted, ok := derived.AcceptedObservation()
	if !ok {
		return nil
	}
	if !reflect.DeepEqual(observation.PowerSource, meter.Source) || observation.BeforeNative.NodeName != cost.Node.Name || observation.BeforeNative.NodeUID != cost.Node.UID {
		return errors.New("retained power source/native physical Node mismatch")
	}
	if err = o.validatePowerNativeProfiles(observation.BeforeNative); err != nil {
		return err
	}
	beforeState := nativeCostState(observation.BeforeNative)
	targetState := nativeCostState(observation.AfterNative)
	if len(cost.Trials) > 0 && (!slices.Equal(cost.BeforeState, beforeState) || !slices.Equal(cost.TargetState, targetState)) {
		return errors.New("directed trials have incompatible actual loaded allocations")
	}
	cost.BeforeState = beforeState
	cost.TargetState = targetState
	cost.Trials = append(cost.Trials, serving.OperationTrial{Duration: time.Duration(accepted.TauMilliseconds() * float64(time.Millisecond)), Joules: accepted.TransitionWallJoules()})
	return nil
}
func nativeCostState(node *transitioncost.NativeNodeObservation) []string {
	state := make([]string, 0, len(node.Instances))
	for _, i := range node.Instances {
		m := i.State.Metadata
		state = append(state, serving.SchedulerInstanceState(serving.SchedulerInstance{Profile: &serving.Profile{ID: m.ProfileID}, Allocation: serving.SchedulerAllocation{CPUs: m.Runtime.CPUAffinity, GPUUUID: m.Runtime.GPUUUID, Threads: m.Runtime.IntraOpThreads, MemoryBytes: uint64(i.MemoryLimitBytes)}}))
	}
	slices.Sort(state)
	return state
}

type powerPreparationWire struct {
	SchemaVersion string `json:"schemaVersion"`
	Prepared      *struct {
		Namespace              string                        `json:"namespace"`
		TargetNode             serving.NodeReference         `json:"targetNode"`
		ObserverNodeName       string                        `json:"observerNodeName"`
		EvidenceStore          string                        `json:"evidenceStore"`
		DeviceClass            string                        `json:"deviceClass"`
		ExecutionPath          string                        `json:"executionPath"`
		ServingService         string                        `json:"servingService"`
		ServingContainer       string                        `json:"servingContainer"`
		PowerSource            metricobserver.SourceIdentity `json:"powerSource"`
		PowerRequest           metricobserver.Request        `json:"powerRequestNanoseconds"`
		FactPollInterval       time.Duration                 `json:"factPollIntervalNanoseconds"`
		FactObservationTimeout time.Duration                 `json:"factObservationTimeoutNanoseconds"`
		Policy                 transitioncost.Policy         `json:"policy"`
		Directions             []struct {
			FrozenContract           transitioncost.FrozenContract `json:"frozenContract"`
			TargetIdleBaselineResult json.RawMessage               `json:"targetIdleBaselineResult"`
			Actuator                 json.RawMessage               `json:"actuator"`
			ActuationPolicy          json.RawMessage               `json:"actuationPolicy"`
		} `json:"directions"`
	} `json:"prepared"`
}

func (o *Operations) validatePowerNativeProfiles(node *transitioncost.NativeNodeObservation) error {
	profiles := o.observer.Profiles()
	for _, instance := range node.Instances {
		var profile *serving.Profile
		for index := range profiles {
			if profiles[index].ID == instance.State.Metadata.ProfileID {
				profile = &profiles[index]
				break
			}
		}
		if profile == nil || profile.RuntimeMetadata == nil || profile.Condition.NodeUID != node.NodeUID || instance.Image != profile.Condition.RuntimeImage {
			return errors.New("power native loaded state lacks matching verified profile/image/physical Node")
		}
		actual := instance.State.Metadata.Runtime
		expected := *profile.RuntimeMetadata
		expected.ModelFiles = deliveredModelFiles(profile.Condition.Artifact.Files)
		if !reflect.DeepEqual(actual, expected) {
			return errors.New("power actual native runtime/provider/allocation differs from verified profile")
		}
		request, err := resource.ParseQuantity(profile.Condition.CPU.Request)
		if err != nil {
			return err
		}
		limit, err := resource.ParseQuantity(profile.Condition.CPU.Limit)
		if err != nil {
			return err
		}
		if request.MilliValue() != instance.CPURequestMilli || limit.MilliValue() != instance.CPULimitMilli {
			return errors.New("power native CPU quota differs from measured profile")
		}
	}
	return nil
}
