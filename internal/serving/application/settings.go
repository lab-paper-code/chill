package application

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type SettingsInput struct {
	serving.SchedulerSettingsInput
	StaticRouting            json.RawMessage `json:"staticRouting"`
	StaticRoutingValidatedAt json.RawMessage `json:"staticRoutingValidatedAt"`
}

func (o *SchedulerService) ChangeSchedulerSettings(ctx context.Context, input SettingsInput,
	versionMatch string, preconditionValid bool, actorUsername, actorUID string,
) (serving.SchedulerSettingsChange, string, error) {
	for _, field := range [...]struct {
		name  string
		value json.RawMessage
	}{{"staticRouting", input.StaticRouting}, {"staticRoutingValidatedAt", input.StaticRoutingValidatedAt}} {
		if field.value != nil {
			return serving.SchedulerSettingsChange{}, "", &serving.Error{
				Code:    "InvalidBody",
				Message: field.name + " is read-only; use scheduler operations",
				Field:   field.name,
			}
		}
	}
	settings, err := input.SchedulerSettingsInput.Settings()
	if err != nil {
		return serving.SchedulerSettingsChange{}, "", &serving.Error{
			Code:    "InvalidBody",
			Message: err.Error(),
			Field:   "settings",
		}
	}
	current := map[string]bool{}
	for _, d := range o.execution.SchedulerInputs(time.Now).Workloads {
		current[d.UID] = true
	}
	// The stored list names only current Workloads.
	settings.AutonomousWorkloads = slices.DeleteFunc(slices.Clone(settings.AutonomousWorkloads),
		func(uid string) bool { return !current[uid] },
	)
	if err := serving.SchedulerFixedBatchAutonomy(
		o.Controller.State().Snapshot, settings.AutonomousWorkloads,
	); err != nil {
		return serving.SchedulerSettingsChange{}, "", &serving.Error{
			Code:    "InvalidBody",
			Message: err.Error(),
			Field:   "autonomousWorkloads",
		}
	}
	o.Lock.Lock()
	defer o.Lock.Unlock()
	cm, config, err := SchedulerConfigMap(ctx, o.settingsKube, o.opts.ServingNamespace,
		o.opts.SchedulerConfigMap, o.opts.SchedulerConfigKey)
	if err != nil {
		return serving.SchedulerSettingsChange{}, "", err
	}
	if !preconditionValid || versionMatch != cm.ResourceVersion {
		return serving.SchedulerSettingsChange{}, "", &serving.Error{
			Code:    "SettingsConflict",
			Message: "settings changed; GET settings before submitting again",
		}
	}
	username, uid := actorUsername, actorUID
	now := time.Now().UTC()
	change := serving.SchedulerSettingsChange{
		ActorUsername: username, ActorUID: uid, Before: o.Controller.Settings(), After: settings,
		ValidatedAt: now, Affected: []serving.NodeReference{},
	}
	for _, node := range o.Controller.State().Snapshot.Nodes {
		change.Affected = append(change.Affected, node.Node)
	}
	// Persist intent before touching either ConfigMap or running policy.
	if err := o.observationLog.SchedulerRecord(serving.SchedulerObservation{At: now, Settings: &change}); err != nil {
		return serving.SchedulerSettingsChange{}, "", err
	}
	version, err := persistSchedulerSettings(ctx, o.settingsKube, cm, config, o.opts.SchedulerConfigKey, settings)
	if apierrors.IsConflict(err) {
		err = &serving.Error{
			Code:    "SettingsConflict",
			Message: "settings changed during update; GET settings before submitting again",
		}
	}
	if err == nil {
		// Only the scheduler service's full refresh can accept a configuration. Applying
		// settings alone could enable a catalog whose selection was removed by
		// a previously persisted change that has not refreshed successfully.
		err = o.refreshLocked(ctx)
	}
	if err != nil {
		change.Error = err.Error()
	} else {
		applied := time.Now().UTC()
		change.AppliedAt = &applied
	}
	// Failure after refresh does not imply rollback: the selected policy
	// may already be active even when its completion record cannot be appended.
	recordErr := o.observationLog.SchedulerRecord(
		serving.SchedulerObservation{At: time.Now().UTC(), Settings: &change},
	)
	if err = errors.Join(err, recordErr); err != nil {
		return serving.SchedulerSettingsChange{}, "", err
	}

	// Freeze body and ETag under the lock, but release it before potentially blocking HTTP writes.
	return change, version, nil
}

// schedulerConfigMap reads the scheduler ConfigMap and its configuration object.
func SchedulerConfigMap(
	ctx context.Context, kube kubernetes.Interface, namespace, name, key string,
) (*core.ConfigMap, map[string]json.RawMessage, error) {
	cm, err := kube.CoreV1().ConfigMaps(namespace).Get(ctx, name, meta.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	var config map[string]json.RawMessage
	if err = json.Unmarshal([]byte(cm.Data[key]), &config); err != nil {
		return nil, nil, err
	}
	if config == nil {
		return nil, nil, errors.New("scheduler configuration must be an object")
	}
	return cm, config, nil
}

// persistSchedulerSettings writes settings into config and updates the
// ConfigMap read with it, so a concurrent writer fails the update.
func persistSchedulerSettings(
	ctx context.Context, kube kubernetes.Interface, cm *core.ConfigMap, config map[string]json.RawMessage, key string,
	settings serving.SchedulerSettings,
) (string, error) {
	if err := settings.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return "", err
	}
	config["settings"] = raw
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	cm.Data[key] = string(encoded)
	updated, err := kube.CoreV1().ConfigMaps(cm.Namespace).Update(ctx, cm, meta.UpdateOptions{})
	if err != nil {
		return "", err
	}
	return updated.ResourceVersion, nil
}
