package kube

import (
	"context"
	"encoding/json"
	"errors"

	"slices"
	"sync"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CatalogSource supplies independent reporting and accepted catalog inputs.
type CatalogSource interface {
	Read(context.Context) (queryapi.CoverageCatalogRead, error)
}

type CoverageService struct {
	Reader           client.Reader
	Objects          evidencestore.LocationReader
	Catalog          CatalogSource
	Decoder          queryapi.OccupancyEvidenceDecoder
	Inventory        func(context.Context) (queryapi.InventoryView, error)
	Compatibility    func(context.Context) (queryapi.ModelCompatibilityMatrixView, error)
	Authorize        func(string) error
	RequestTimeout   time.Duration
	MaxRequestBytes  int64
	MaxEvidenceBytes int64
	cache            measurementCoverageCache
}

type measurementCoverageCache struct {
	mu         sync.Mutex
	namespaces map[string]*measurementCoverageEntry
}
type measurementCoverageEntry struct {
	base        *queryapi.CoverageBase
	refreshing  bool
	attempted   bool
	err         error
	lastAttempt time.Time
	refreshed   chan struct{}
}

func (o *CoverageService) Coverage(
	ctx context.Context, namespace string, query queryapi.CoverageQuery, sharePaths bool,
) (json.RawMessage, error) {
	if err := o.Authorize(namespace); err != nil {
		return nil, err
	}
	cache := &o.cache
	cache.mu.Lock()
	if cache.namespaces == nil {
		cache.namespaces = map[string]*measurementCoverageEntry{}
	}
	entry := cache.namespaces[namespace]
	if entry == nil {
		entry = &measurementCoverageEntry{}
		cache.namespaces[namespace] = entry
	}
	base := entry.base
	fresh := base != nil && time.Since(base.Oldest()) <= 30*time.Second && len(base.Failures) == 0
	refreshDue := base == nil || time.Since(entry.lastAttempt) >= 30*time.Second
	if !fresh && !entry.refreshing && refreshDue {
		entry.refreshing = true
		entry.lastAttempt = time.Now()
		entry.refreshed = make(chan struct{})
		go func(previous *queryapi.CoverageBase) {
			refreshCtx, cancel := context.WithTimeout(context.Background(), o.RequestTimeout)
			defer cancel()
			updated, err := o.readCoverage(refreshCtx, namespace, previous)
			cache.mu.Lock()
			defer cache.mu.Unlock()
			entry.refreshing = false
			close(entry.refreshed)
			entry.attempted = true
			entry.err = err
			if updated != nil {
				entry.base = updated
			}
		}(base)
	}
	attempted, lastErr, refreshed, refreshing := entry.attempted, entry.err, entry.refreshed, entry.refreshing
	cache.mu.Unlock()
	// Once the stale allowance is exhausted, finish the in-flight refresh before
	// rejecting. The first request after an idle period must not fail merely
	// because it is the request that starts source refresh.
	if base != nil && time.Since(base.Oldest()) > 5*time.Minute && refreshing {
		select {
		case <-refreshed:
			cache.mu.Lock()
			base, lastErr = entry.base, entry.err
			cache.mu.Unlock()
		case <-ctx.Done():
			return nil, &queryapi.MeasurementError{Code: "CoverageUnavailable", Message: "측정 커버리지 입력 갱신을 완료하지 못했습니다"}
		}
	}
	if base == nil {
		code := "CoverageWarming"
		if attempted && lastErr != nil {
			code = "CoverageUnavailable"
		}
		return nil, &queryapi.MeasurementError{Code: code, Message: "측정 커버리지 입력을 준비하지 못했습니다"}
	}
	if time.Since(base.Oldest()) > 5*time.Minute {
		return nil, &queryapi.MeasurementError{Code: "CoverageUnavailable", Message: "측정 커버리지 입력의 유효 시간이 지났습니다"}
	}
	if query.Node != "" {
		found := false
		for _, class := range base.Inventory.Classes {
			for _, node := range class.Nodes {
				found = found || node.Name == query.Node
			}
		}
		if !found {
			return nil, &queryapi.MeasurementError{
				Code:    "InvalidCoverageQuery",
				Message: "현재 inventory의 Node 이름이 필요합니다",
			}
		}
	}
	response := queryapi.BuildMeasurementCoverage(base, namespace, query)
	var payload json.RawMessage
	var err error
	if query.Summary {
		payload, err = queryapi.MeasurementSummaryJSON(response, false, sharePaths)
	} else {
		payload, err = json.Marshal(response)
	}
	if err == nil && int64(len(payload)) > o.MaxRequestBytes {
		return nil, &queryapi.MeasurementError{Code: "CoverageUnavailable", Message: "커버리지 응답이 크기 한도를 초과했습니다"}
	}
	return payload, err
}

func (o *CoverageService) readCoverage(
	ctx context.Context, namespace string, previous *queryapi.CoverageBase,
) (*queryapi.CoverageBase, error) {
	next := &queryapi.CoverageBase{Checked: map[string]time.Time{}, Failures: map[string]error{}}
	if previous != nil {
		*next = *previous
		next.Checked = map[string]time.Time{}
		for kind, at := range previous.Checked {
			next.Checked[kind] = at
		}
		next.Failures = map[string]error{}
	}
	// Independent source reads share the deadline; one namespace refresh owns them.
	type sourceResult struct {
		kind string
		err  error
		at   time.Time
	}
	results := make(chan sourceResult, len(queryapi.CoverageSourceKinds))
	go func() {
		value, err := o.Inventory(ctx)
		if err == nil {
			next.Inventory = value
		}
		results <- sourceResult{"inventory", err, time.Now().UTC()}
	}()
	go func() {
		value, err := o.Compatibility(ctx)
		if err == nil {
			next.Compatibility = value
		}
		results <- sourceResult{"compatibility", err, time.Now().UTC()}
	}()

	go func() {
		var value api.BatchEnergyOptimizationList
		err := o.Reader.List(ctx, &value, client.InNamespace(namespace))
		if err == nil {
			next.Requests = value
		}
		results <- sourceResult{"BEO", err, time.Now().UTC()}
	}()
	go func() {
		var value api.ProfilingRunList
		err := o.Reader.List(ctx, &value, client.InNamespace(namespace))
		if err == nil {
			next.Runs = value
		}
		results <- sourceResult{"Run", err, time.Now().UTC()}
	}()
	go func() {
		var value api.DispatchOccupancyMeasurementList
		err := o.Reader.List(ctx, &value, client.InNamespace(namespace))
		projected := make(map[string]queryapi.OccupancyEvidenceProjection, len(value.Items))
		if err == nil {
			for _, task := range value.Items {
				var result queryapi.OccupancyEvidenceProjection
				result, err = ProjectDispatchOccupancy(ctx, o.Objects, o.MaxEvidenceBytes, task, o.Decoder)
				if err != nil {
					break
				}
				projected[string(task.UID)] = result
			}
		}
		if err == nil {
			next.Occupancies, next.OccupancyResults = value, projected
		}
		results <- sourceResult{"DOM", err, time.Now().UTC()}
	}()
	go func() {
		var datasets api.DatasetList
		var preprocessors api.PreprocessorList
		err := o.Reader.List(ctx, &datasets)
		if err == nil {
			err = o.Reader.List(ctx, &preprocessors)
		}
		if err == nil {
			next.Datasets, next.Preprocessors = coverageInputFacts(datasets.Items, preprocessors.Items)
			next.DatasetsResourceVersion, next.PreprocessorsResourceVersion = datasets.ResourceVersion, preprocessors.ResourceVersion
		}
		results <- sourceResult{"input-contract", err, time.Now().UTC()}
	}()
	go func() {
		// Reporting may retain stale facts, but adoption needs this refresh's
		// confirmed selection, never the previous successful read.
		next.AcceptedOccupancy = nil
		next.ObservedOccupancy = nil
		read, err := o.Catalog.Read(ctx)
		value := read.Reporting
		at := read.SourceCheckedAt
		if err == nil && at.IsZero() {
			err = errors.New("catalog source has not been confirmed")
		}
		if err == nil {
			next.Catalog = value
			next.Adopted = map[string]bool{}
			if read.Accepted != nil {
				for _, profile := range read.Accepted.Profiles {
					next.Adopted[profile.ID] = true
				}
			}
			if value.SourceError != "" {
				err = errors.New(value.SourceError)
			}
			if err == nil && read.Accepted != nil {
				next.AcceptedOccupancy = read.AcceptedOccupancy
				next.ObservedOccupancy = read.ObservedOccupancy
			}
		}
		results <- sourceResult{queryapi.CoverageCatalogSource, err, at}
	}()

	var failures []error
	for range queryapi.CoverageSourceKinds {
		result := <-results
		if result.err != nil {
			next.Failures[result.kind] = result.err
			failures = append(failures, result.err)
			if result.kind == queryapi.CoverageCatalogSource && !result.at.IsZero() {
				next.Checked[result.kind] = result.at
			}
		} else {
			next.Checked[result.kind] = result.at
		}
	}
	if len(next.Checked) != len(queryapi.CoverageSourceKinds) {
		return nil, errors.Join(failures...)
	}
	// Stable source order, not LIST iteration order, determines the projection.
	next.Requests.Items = append([]api.BatchEnergyOptimization{}, next.Requests.Items...)
	slices.SortFunc(next.Requests.Items, func(a, b api.BatchEnergyOptimization) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return next, errors.Join(failures...)
}
