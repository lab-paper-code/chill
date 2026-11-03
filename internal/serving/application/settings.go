package application

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type SettingsDependencies struct {
	Runtime                   *serving.Runtime
	Scheduler                 *servingkube.SchedulerController
	Lock                      *sync.Mutex
	Kube                      kubernetes.Interface
	Namespace, ConfigMap, Key string
	ObservationLog            *serving.ObservationLog
	Refresh                   func(context.Context) error
}
type SettingsInput struct {
	serving.SchedulerSettingsInput
	StaticRouting            json.RawMessage `json:"staticRouting"`
	StaticRoutingValidatedAt json.RawMessage `json:"staticRoutingValidatedAt"`
}

func ChangeSchedulerSettings(ctx context.Context, dependencies SettingsDependencies, input SettingsInput,
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
	for _, d := range dependencies.Runtime.SchedulerInputs(time.Now).Workloads {
		current[d.UID] = true
	}
	// The stored list names only current Workloads.
	settings.AutonomousWorkloads = slices.DeleteFunc(slices.Clone(settings.AutonomousWorkloads),
		func(uid string) bool { return !current[uid] },
	)
	if err := serving.SchedulerFixedBatchAutonomy(
		dependencies.Scheduler.State().Snapshot, settings.AutonomousWorkloads,
	); err != nil {
		return serving.SchedulerSettingsChange{}, "", &serving.Error{
			Code:    "InvalidBody",
			Message: err.Error(),
			Field:   "autonomousWorkloads",
		}
	}
	dependencies.Lock.Lock()
	defer dependencies.Lock.Unlock()
	cm, config, err := SchedulerConfigMap(ctx, dependencies.Kube, dependencies.Namespace,
		dependencies.ConfigMap, dependencies.Key)
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
		ActorUsername: username, ActorUID: uid, Before: dependencies.Scheduler.Settings(), After: settings,
		ValidatedAt: now, Affected: []serving.NodeReference{},
	}
	for _, node := range dependencies.Scheduler.State().Snapshot.Nodes {
		change.Affected = append(change.Affected, node.Node)
	}
	// Persist intent before touching either ConfigMap or running policy.
	if err := dependencies.ObservationLog.SchedulerRecord(serving.SchedulerObservation{At: now, Settings: &change}); err != nil {
		return serving.SchedulerSettingsChange{}, "", err
	}
	version, err := persistSchedulerSettings(ctx, dependencies.Kube, cm, config, dependencies.Key, settings)
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
		err = dependencies.Refresh(ctx)
	}
	if err != nil {
		change.Error = err.Error()
	} else {
		applied := time.Now().UTC()
		change.AppliedAt = &applied
	}
	// Failure after refresh does not imply rollback: the selected policy
	// may already be active even when its completion record cannot be appended.
	recordErr := dependencies.ObservationLog.SchedulerRecord(
		serving.SchedulerObservation{At: time.Now().UTC(), Settings: &change},
	)
	if err = errors.Join(err, recordErr); err != nil {
		return serving.SchedulerSettingsChange{}, "", err
	}

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
