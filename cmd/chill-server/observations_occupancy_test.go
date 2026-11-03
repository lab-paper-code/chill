package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

func TestOccupancyObservationConfiguredWindow(t *testing.T) {
	service := &schedulerService{}
	service.configuration.Store(&schedulerConfiguration{
		DispatchOccupancy: &dispatchOccupancyPolicy{
			AggregationWindowSeconds: 120,
		},
	})
	from := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	for _, duration := range []time.Duration{120*time.Second + time.Nanosecond, time.Hour} {
		_, err := service.occupancyObservations(context.Background(), from, from.Add(duration))
		var limit *httpapi.OccupancyWindowError
		if !errors.As(err, &limit) || limit.MaximumWindowSeconds != 120 {
			t.Fatal("configured observation window was not enforced before scanning", err)
		}
	}
	config := schedulerTestConfiguration()
	policy := syntheticOccupancyPolicy()
	config.DispatchOccupancy = &policy
	service = schedulerTestService(t, config)
	if _, err := service.occupancyObservations(context.Background(), from, from.Add(120*time.Second)); err != nil {
		t.Fatal("exact configured aggregation window was rejected", err)
	}
}
