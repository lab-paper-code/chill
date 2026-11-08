package kube

import (
	"context"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestSchedulerAcceptedPolicyFreezesConfiguredBudgetBeforeNextTick(t *testing.T) {
	for _, update := range []string{"settings", "configuration"} {
		t.Run(update, func(t *testing.T) {
			c, _, physical := acknowledgedHandoffFixture(t)
			settings := schedulerEmptyPoolController(t).Settings()
			c.config.Settings = settings
			definition := c.runtime.SchedulerInputs(time.Now).Workloads[0]
			started, release := make(chan serving.Dispatch, 2), make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			physical.Execute = func(_ context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
				started <- dispatch
				<-release
				return serving.BackendResult{Finished: true}, nil
			}
			if err := c.runtime.InstallInstances([]serving.ReadyInstance{physical}); err != nil {
				t.Fatal(err)
			}
			c.runtime.SetPathBudgets([]serving.PathBudget{{WorkloadUID: definition.UID, DefinitionID: definition.Identity(),
				Source: serving.PathBudgetObserved, TotalMS: 19, PostMS: 7, JudgmentPercentile: 99.5}})
			accept := func() *serving.Call {
				t.Helper()
				receipt, err := c.runtime.Receive(definition.Name, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				call, err := c.runtime.Accept(&receipt, managerSample(t))
				if err != nil {
					t.Fatal(err)
				}
				return call
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			first := accept()
			select {
			case dispatch := <-started:
				if dispatch.CompletionMarginMS != 7 {
					t.Fatalf("initial observed POST missing: %+v", dispatch)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			enabled := false
			settings.ObservedPathBudgets, settings.CompletionMarginMS = &enabled, 13
			var err error
			if update == "settings" {
				err = c.AcceptSettings(context.Background(), settings, c.config.Preparations)
			} else {
				config := c.config
				config.Settings = settings
				err = c.SetConfiguration(config)
			}
			if err != nil {
				t.Fatal(err)
			}
			second := accept() // No Tick or later application fallback setter intervenes.
			select {
			case dispatch := <-started:
				if dispatch.CompletionMarginMS != 13 {
					t.Fatalf("accepted POST changed across %s boundary: %+v", update, dispatch)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			outstanding := c.runtime.SchedulerInputs(time.Now).Outstanding
			if len(outstanding) != 2 {
				t.Fatalf("missing accepted requests: %+v", outstanding)
			}
			seen := map[float64]bool{}
			for _, request := range outstanding {
				if request.CompletionMarginMS == nil || request.TotalMarginMS == nil {
					t.Fatalf("accepted budget not frozen: %+v", request)
				}
				post := *request.CompletionMarginMS
				wantTotal := map[float64]float64{7: 19, 13: 13}[post]
				if wantTotal == 0 || *request.TotalMarginMS != wantTotal {
					t.Fatalf("configured TOTAL or old request changed: %+v", request)
				}
				seen[post] = true
			}
			if len(seen) != 2 {
				t.Fatal("old and new acceptances did not retain separate policies")
			}
			close(release)
			released = true
			for _, call := range []*serving.Call{first, second} {
				if _, err := call.Wait(ctx); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
