package application

import (
	"context"
	"errors"
	"maps"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
)

// settingsPreparations derives policy from accepted typed inputs only. Desired
// ConfigMap fields and profiles still undergoing source preparation are excluded.
func (o *SchedulerService) settingsPreparations(settings serving.SchedulerSettings) (*ProfilesView, map[string]servingstate.PrepareSpec, error) {
	accepted := o.Profiles.Load()
	if o.Controller == nil || o.configuration.Load() == nil || accepted == nil {
		return nil, nil, errors.New("scheduler configuration has not been accepted")
	}
	published := *accepted
	published.Preparations = maps.Clone(accepted.Preparations)
	preparations := make(map[string]servingstate.PrepareSpec, len(accepted.Preparations))
	for index := range accepted.Profiles {
		profile := &accepted.Profiles[index]
		spec, ok := accepted.Preparations[profile.ID]
		if !ok {
			continue
		}
		queueBytes, err := measuredQueueBytes(*profile, settings.ServingMemoryBytes)
		if err != nil {
			return nil, nil, err
		}
		spec.Config.MaxQueueBytes = int(queueBytes)
		published.Preparations[profile.ID] = spec
		spec.Instance.Profile = profile
		preparations[profile.ID] = spec
	}
	if len(preparations) != len(accepted.Preparations) {
		return nil, nil, errors.New("accepted preparation profile unavailable")
	}
	return &published, preparations, nil
}

// applySchedulerSettings preserves all accepted source/private fields. Its
// caller holds Lock and has persisted the validated settings, not a catalog.
func (o *SchedulerService) applySchedulerSettings(ctx context.Context, settings serving.SchedulerSettings, published *ProfilesView, preparations map[string]servingstate.PrepareSpec) error {
	if err := o.Controller.AcceptSettings(ctx, settings, preparations); err != nil {
		return err
	}
	config := *o.configuration.Load()
	config.Settings = settings
	o.execution.SetCompletionMargin(settings.CompletionMarginMS)
	o.configuration.Store(&config)
	// ConfigVersion and resourceVersion still name the last full acceptance.
	o.Profiles.Store(published)
	o.occupancy.mu.Lock()
	defer o.occupancy.mu.Unlock()
	if snapshot := o.occupancy.published.Load(); snapshot != nil {
		input := *snapshot.input
		input.configuration, input.catalog = &config, published
		// Keep qualified rolling facts, but resolve DOM/default again with the
		// new policy. Old fallback values must not become measurement evidence.
		var measured []serving.DispatchOccupancy
		for _, value := range snapshot.selected {
			if value.Provenance != nil && value.Provenance.Source == measuredOccupancySource {
				measured = append(measured, value)
			}
		}
		o.occupancy.published.Store(&rollingOccupancySnapshot{
			input: &input, selected: resolveOccupancy(&input, measured), estimates: snapshot.estimates,
		})
		select {
		case o.occupancy.changed <- struct{}{}:
		default:
		}
	}
	return nil
}
