package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

type coverageWaitContext struct {
	context.Context
	waiting chan struct{}
}

func (c coverageWaitContext) Done() <-chan struct{} {
	close(c.waiting)
	return c.Context.Done()
}

func TestCoverageExpiredSnapshotWaitsForActiveRefresh(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			base := coverageSizeBase()
			for kind := range base.checked {
				base.checked[kind] = time.Now().Add(-6 * time.Minute)
			}
			entry := &measurementCoverageEntry{base: base, refreshing: true, refreshed: make(chan struct{})}
			service := &measurementService{
				opts: options{servingNamespace: coverageSizeNamespace, maxRequestBytes: 8 << 20},
				coverageCache: measurementCoverageCache{
					namespaces: map[string]*measurementCoverageEntry{coverageSizeNamespace: entry},
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			waiting := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				payload, err := service.coverage(
					coverageWaitContext{Context: ctx, waiting: waiting}, coverageSizeNamespace,
					httpapi.MeasurementCoverageQuery{Batches: []int{1, 4}},
				)
				if err == nil {
					var view struct {
						Freshness string `json:"freshness"`
					}
					err = json.Unmarshal(payload, &view)
					if err == nil && view.Freshness != "fresh" {
						err = errors.New("refresh success returned expired snapshot")
					}
				}
				result <- err
			}()
			select {
			case <-waiting:
			case err := <-result:
				t.Fatalf("returned before active refresh completed: %v", err)
			case <-ctx.Done():
				t.Fatal("request did not wait for refresh")
			}
			service.coverageCache.mu.Lock()
			if fail {
				entry.err = errors.New("source unavailable")
			} else {
				entry.base = coverageSizeBase()
			}
			entry.refreshing = false
			close(entry.refreshed)
			service.coverageCache.mu.Unlock()
			if err := <-result; (err != nil) != fail {
				t.Fatalf("refresh failure=%v, got %v", fail, err)
			}
		})
	}
}

func TestCoverageWithinStaleWindowReturnsWhileRefreshing(t *testing.T) {
	base := coverageSizeBase()
	for kind := range base.checked {
		base.checked[kind] = time.Now().Add(-time.Minute)
	}
	entry := &measurementCoverageEntry{base: base, refreshing: true, refreshed: make(chan struct{})}
	service := &measurementService{
		opts: options{servingNamespace: coverageSizeNamespace, maxRequestBytes: 8 << 20},
		coverageCache: measurementCoverageCache{
			namespaces: map[string]*measurementCoverageEntry{coverageSizeNamespace: entry},
		},
	}
	payload, err := service.coverage(context.Background(), coverageSizeNamespace, httpapi.MeasurementCoverageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Freshness string `json:"freshness"`
	}
	if err := json.Unmarshal(payload, &view); err != nil || view.Freshness != "stale" {
		t.Fatalf("in-flight refresh hid usable stale snapshot: %s, %v", payload, err)
	}
}
