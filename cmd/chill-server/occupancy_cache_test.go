package main

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

const rollingDOMTest = "dom"

func rollingOccupancyFixture() *rollingOccupancyInput {
	policy := syntheticOccupancyPolicy()
	settings := serving.SchedulerSettings{DefaultDispatchOccupancyNanos: 2000000}
	profile := serving.Profile{ID: "profile", NodeName: "node", Batches: []serving.BatchProfile{{Batch: 1}, {Batch: 2}}}
	profile.Condition.NodeUID = "node-uid"
	profile.Condition.LogicalModel = occupancyModelTest
	profile.Condition.PowerMode = "fixed"
	profile.Condition.Artifact.ExecutionPath = "cpu"
	other := profile
	other.ID, other.NodeName, other.Condition.NodeUID = "other", "other-node", "other-uid"
	return &rollingOccupancyInput{
		configuration: &schedulerConfiguration{Settings: settings, DispatchOccupancy: &policy},
		catalog: &httpapi.ProfilesView{ConfigVersion: "accepted",
			CatalogView: servingkube.CatalogView{Profiles: []serving.Profile{profile, other}}},
		profiles: map[string]*serving.Profile{profile.ID: &profile, other.ID: &other},
		legacy: []serving.DispatchOccupancy{{Node: serving.NodeReference{Name: "node", UID: "node-uid"},
			PowerMode: "fixed", Model: occupancyModelTest, PerRequest: 270625, Source: "accepted-dom.json"}},
	}
}

func rollingEstimate(
	input *rollingOccupancyInput, profile string, batch, pairs int, median float64, now time.Time,
) serving.OccupancyEstimate {
	p := input.profiles[profile]
	return serving.OccupancyEstimate{
		Node:  serving.NodeReference{Name: p.NodeName, UID: p.Condition.NodeUID},
		Model: p.Condition.LogicalModel, PowerMode: p.Condition.PowerMode,
		ExecutionPath: p.Condition.Artifact.ExecutionPath, Batch: batch, Pairs: pairs,
		Profile:    serving.OccupancyProfile{ProfileID: profile},
		Statistics: &serving.OccupancyStatistics{Median: median, Mean: median},
		Window:     serving.OccupancyWindow{Start: now.Add(-120 * time.Second), End: now},
	}
}

func TestRollingOccupancyDefaultDOMMeasuredAndSignedFacts(t *testing.T) {
	input := rollingOccupancyFixture()
	var cache rollingOccupancyCache
	cache.configure(input)
	initial := cache.selected()
	if len(initial) != 4 || initial[0].Provenance.Source != rollingDOMTest || initial[1].Batch != 2 ||
		initial[2].Provenance.Source != "default" || initial[2].PerRequest != 2000000 || initial[3].PerRequest != 2000000 {
		t.Fatalf("first computation did not use accepted DOM/configured defaults: %+v", initial)
	}
	now := time.Now().UTC()
	estimates := []serving.OccupancyEstimate{
		rollingEstimate(input, "profile", 1, 100, 198304.75, now),
		rollingEstimate(input, "profile", 2, 99, 1, now),
		rollingEstimate(input, "other", 1, 100, -25, now),
	}
	err := cache.refresh(context.Background(), input, now,
		func(ctx context.Context, _ *rollingOccupancyInput, from, to time.Time) ([]serving.OccupancyEstimate, error) {
			deadline, ok := ctx.Deadline()
			if !ok || deadline.After(time.Now().Add(30*time.Second)) || to != now || to.Sub(from) != 120*time.Second {
				t.Fatal("configured rolling window/deadline lost")
			}
			return estimates, nil
		})
	selected := cache.selected()
	if err != nil || selected[0].PerRequest != 198305 || selected[0].Provenance.Source != "measured" ||
		selected[0].ProfileID != "profile" || selected[0].ExecutionPath != "cpu" || selected[0].Batch != 1 ||
		selected[0].Provenance.Pairs != 100 || selected[0].Provenance.Window.End != now ||
		selected[0].Provenance.UpdatedAt.IsZero() || selected[0].Provenance.Value != 198305 ||
		selected[1].Provenance.Source != rollingDOMTest || selected[2].PerRequest != 0 ||
		selected[2].Provenance.Source != "measured" {
		t.Fatalf("wrong qualified selection: %+v %v", selected, err)
	}
	if initial[0].Provenance.Source != rollingDOMTest || cache.published.Load().estimates[2].Statistics.Median != -25 {
		t.Fatal("publication mutated prior snapshot or censored signed facts")
	}
	selectionLimit := &serving.Error{Code: "OccupancySelectionLimit", Message: "request bound"}
	if err := cache.refresh(context.Background(), input, now,
		func(context.Context, *rollingOccupancyInput, time.Time, time.Time) ([]serving.OccupancyEstimate, error) {
			return estimates, selectionLimit
		}); !errors.Is(err, selectionLimit) || cache.selected()[0].Provenance.Source != rollingDOMTest ||
		cache.published.Load().estimates != nil {
		t.Fatal("partial/error scan published facts or retained stale measurement", err)
	}
}

func TestRollingOccupancyRejectsPublicationAcrossAcceptedConfiguration(t *testing.T) {
	input := rollingOccupancyFixture()
	var cache rollingOccupancyCache
	cache.configure(input)
	next := rollingOccupancyFixture()
	next.catalog.ConfigVersion = "next"
	next.configuration.Settings.DefaultDispatchOccupancyNanos = 42
	now := time.Now().UTC()
	if err := cache.refresh(context.Background(), input, now,
		func(context.Context, *rollingOccupancyInput, time.Time, time.Time) ([]serving.OccupancyEstimate, error) {
			cache.configure(next)
			return []serving.OccupancyEstimate{rollingEstimate(input, "profile", 1, 100, 1, now)}, nil
		}); err != nil {
		t.Fatal(err)
	}
	if cache.published.Load().input != next || cache.selected()[0].Provenance.Source != rollingDOMTest ||
		cache.selected()[2].PerRequest != 42 {
		t.Fatal("old scan overwrote newly accepted configuration")
	}
}

func TestRollingOccupancySingleWorkerCancellationAndDuty(t *testing.T) {
	for _, tc := range []struct{ refresh, elapsed, delay time.Duration }{
		{60 * time.Second, 10 * time.Second, 50 * time.Second},
		{60 * time.Second, 30 * time.Second, 30 * time.Second},
		{60 * time.Second, 45 * time.Second, 45 * time.Second},
	} {
		if got := occupancyRefreshDelay(tc.refresh, tc.elapsed); got != tc.delay {
			t.Fatalf("duty cooldown=%s want %s", got, tc.delay)
		}
	}
	var cache rollingOccupancyCache
	cache.configure(rollingOccupancyFixture())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, finished := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(finished)
		cache.run(ctx, func(
			ctx context.Context, _ *rollingOccupancyInput, _, _ time.Time,
		) ([]serving.OccupancyEstimate, error) {
			calls.Add(1)
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker never began its first asynchronous computation")
	}
	cache.configure(rollingOccupancyFixture())
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop with service context")
	}
	if calls.Load() != 1 {
		t.Fatal("configuration update overlapped observation scans")
	}
}

func TestRollingOccupancyCatalogUsesSameAcceptedSnapshot(t *testing.T) {
	service := &schedulerService{catalog: &servingkube.Catalog{}}
	service.catalogRead.Store(&httpapi.ProfilesView{})
	input := rollingOccupancyFixture()
	service.occupancy.configure(input)
	read, err := service.readCatalog(context.Background())
	if err != nil || read.Accepted == nil || read.Accepted.ConfigVersion != input.catalog.ConfigVersion ||
		!reflect.DeepEqual(read.Accepted.DispatchOccupancy, service.occupancy.selected()) ||
		len(read.DispatchOccupancy) != 0 {
		t.Fatalf("catalog diverged from immutable scheduler sources: %+v %v", read, err)
	}
}
