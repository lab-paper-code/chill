package observation

import (
	"context"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
)

func newObservedRuntime(t *testing.T, ctx context.Context, config serving.RuntimeConfig) (*serving.Runtime, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	return serving.NewRuntime(ctx, config)
}

func schedulerEstimateSettingsForTest() serving.SchedulerSettings {
	return serving.SchedulerSettings{
		DefaultOperationCost: map[string]serving.OperationCostValue{
			"prepare": {Seconds: 852, Joules: 36371}, "remove": {Seconds: 13, Joules: 1014}, "power": {Seconds: 166, Joules: 2229},
		},
		DefaultIdlePower: &serving.IdlePowerDefault{NodeWatts: 54, PerInstanceWatts: 8},
	}
}
