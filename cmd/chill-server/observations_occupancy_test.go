package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestOccupancyObservationConfiguredWindow(t *testing.T) {
	service := &schedulerService{}
	service.configuration.Store(&schedulerConfiguration{
		DispatchOccupancy: &dispatchOccupancyPolicy{MaximumObservationWindowSeconds: 3600},
	})
	from := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	_, err := service.occupancyObservations(context.Background(), from, from.Add(time.Hour+time.Nanosecond))
	var domain *serving.Error
	if !errors.As(err, &domain) || domain.Code != "InvalidQuery" {
		t.Fatal("configured observation window was not enforced", err)
	}
}
