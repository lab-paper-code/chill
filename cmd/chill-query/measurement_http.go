package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func (o *measurementService) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/scheduler/profiles", func(w http.ResponseWriter, r *http.Request) {
		read, err := o.catalog(r.Context())
		if err != nil {
			httpapi.SendQueryError(w, err)
			return
		}
		view := adoptedCatalogView(read)
		view.DisplayStatus = func(p serving.Profile) string { a, _ := assessCatalogProfile(view, p); return a.DisplayStatus }
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err == nil {
			view, err = httpapi.PageProfiles(view, values)
		}
		if err != nil {
			httpapi.SendQueryError(w, &serving.Error{Code: "InvalidQuery", Message: "invalid profile query"})
			return
		}
		payload, err := o.buildProfiles(r.Context(), view)
		if err != nil {
			httpapi.SendQueryError(w, err)
			return
		}
		writeMeasurement(w, payload)
	})
	mux.HandleFunc(
		"GET /api/v1/namespaces/{namespace}/measurement-coverage",
		func(w http.ResponseWriter, r *http.Request) {
			values, err := url.ParseQuery(r.URL.RawQuery)
			var query httpapi.MeasurementCoverageQuery
			if err == nil {
				query, err = httpapi.ParseMeasurementCoverageQuery(values)
			}
			if err != nil {
				httpapi.SendQueryError(w, &serving.Error{Code: "InvalidCoverageQuery", Message: "유효한 query가 필요합니다"})
				return
			}
			payload, err := o.coverage(r.Context(), r.PathValue("namespace"), query)
			if err != nil {
				var domain *serving.Error
				if errors.As(err, &domain) && domain.Code == "CoverageWarming" {
					w.Header().Set("Retry-After", "2")
				}
				httpapi.SendQueryError(w, err)
				return
			}
			writeMeasurement(w, payload)
		},
	)
	mux.HandleFunc(
		"GET /api/v1/namespaces/{namespace}/batch-energy-optimizations",
		func(w http.ResponseWriter, r *http.Request) {
			list, err := o.list(r.Context(), r.PathValue("namespace"))
			if err != nil {
				httpapi.SendQueryError(w, err)
				return
			}
			assessments, err := o.assessList(r.Context(), list)
			if err != nil {
				httpapi.SendQueryError(w, err)
				return
			}
			writeMeasurement(w, projectMeasurementList(list, assessments))

		},
	)
	for _, resource := range []string{"operation-cost-measurements", "dispatch-occupancy-measurements"} {
		for _, suffix := range []string{"", "/{name}"} {
			mux.HandleFunc(
				"GET /api/v1/namespaces/{namespace}/"+resource+suffix,
				func(w http.ResponseWriter, r *http.Request) {
					namespace, name := r.PathValue("namespace"), r.PathValue("name")
					var value any
					var err error
					if resource == "operation-cost-measurements" {
						if name == "" {
							value, err = o.listOperationCosts(r.Context(), namespace)
						} else {
							value, err = o.getOperationCost(r.Context(), namespace, name)
						}
					} else {
						if name == "" {
							value, err = o.listDispatchOccupancies(r.Context(), namespace)
						} else {
							value, err = o.getDispatchOccupancy(r.Context(), namespace, name)
						}
					}
					if err != nil {
						httpapi.SendQueryError(w, err)
						return
					}
					writeMeasurement(w, value)
				},
			)
		}
	}
}

func adoptedCatalogView(read servingkube.CatalogRead) httpapi.ProfilesView {
	if read.Accepted == nil {
		return httpapi.ProfilesView{}
	}
	return catalogView(*read.Accepted)
}

func writeMeasurement(w http.ResponseWriter, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if raw, ok := value.(json.RawMessage); ok {
		_, _ = w.Write(raw)
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func newServingCatalogReader(opts options) (func(context.Context) (servingkube.CatalogRead, error), error) {
	origin, err := url.Parse(opts.servingURL)
	if err != nil ||
		(opts.servingURL != "" && (origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
			origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/"))) {
		return nil, errors.New("serving URL must be one fixed HTTPS origin")
	}
	config := &rest.Config{
		BearerTokenFile: opts.servingTokenFile,
		TLSClientConfig: rest.TLSClientConfig{CAFile: opts.servingCAFile},
	}
	transport, err := rest.TransportFor(config)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       opts.requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return func(ctx context.Context) (value servingkube.CatalogRead, retErr error) {

		if strings.TrimSpace(opts.servingURL) == "" {
			return value, &serving.Error{
				Code:    "SchedulerUnavailable",
				Message: "serving catalog origin is not configured",
			}
		}
		target := *origin
		target.Path = "/api/v1/scheduler/catalog-read"
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return value, err
		}
		response, err := client.Do(request)
		if err != nil {
			return value, &serving.Error{Code: "SchedulerUnavailable", Message: "serving catalog is unavailable"}
		}
		defer func() { retErr = errors.Join(retErr, response.Body.Close()) }()
		if response.StatusCode != http.StatusOK {
			return value, &serving.Error{Code: "SchedulerUnavailable", Message: "serving catalog read was rejected"}
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, opts.maxRequestBytes+1))
		if err != nil {
			return value, err
		}
		if int64(len(raw)) > opts.maxRequestBytes {
			return value, errors.New("serving catalog exceeds query response bound")
		}
		err = json.Unmarshal(raw, &value)
		return value, err
	}, nil
}

// A typed adapter completes preflight before readapi encodes the response.
type measurementPreflightReader struct {
	read func(context.Context, *api.BatchEnergyOptimization) (
		queryapi.BatchEnergyOptimizationPreflightSnapshot, error,
	)
	measurements *measurementService
}

func (p measurementPreflightReader) Read(
	ctx context.Context,
	campaign *api.BatchEnergyOptimization,
) (queryapi.BatchEnergyOptimizationPreflightSnapshot, error) {
	snapshot, err := p.read(ctx, campaign)
	if err != nil {
		return snapshot, err
	}
	return p.measurements.completePreflight(ctx, snapshot), nil
}

// Keep the native Kubernetes list and each complete CR intact; assessment is
// only an additional read-projection field.
func projectMeasurementList(list api.BatchEnergyOptimizationList, assessments []json.RawMessage) any {
	type item struct {
		api.BatchEnergyOptimization
		Assessment json.RawMessage `json:"assessment"`
	}
	items := make([]item, len(list.Items))
	for i := range list.Items {
		items[i] = item{list.Items[i], assessments[i]}
	}
	return struct {
		meta.TypeMeta `json:",inline"`
		meta.ListMeta `json:"metadata,omitempty"`
		Items         []item `json:"items"`
	}{list.TypeMeta, list.ListMeta, items}
}
