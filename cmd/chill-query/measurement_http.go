package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

const (
	measurementSummaryView = "summary"
	measurementViewQuery   = "view"
	measurementLimitQuery  = "limit"
	measurementOffsetQuery = "offset"
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
		if values.Get(measurementViewQuery) == measurementSummaryView {
			summary, err := queryapi.MeasurementSummaryJSON(payload, true)
			if err != nil {
				httpapi.SendQueryError(w, err)
				return
			}
			writeMeasurement(w, summary)
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
			values, err := url.ParseQuery(r.URL.RawQuery)
			var page *httpapi.ProfilePage
			if err == nil {
				page, err = measurementListPage(values)
			}
			if err != nil {
				httpapi.SendQueryError(w, &serving.Error{Code: "InvalidQuery", Message: "invalid measurement list query"})
				return
			}
			list, err := o.list(r.Context(), r.PathValue("namespace"))
			if err != nil {
				httpapi.SendQueryError(w, err)
				return
			}
			if page != nil {
				page.Total = len(list.Items)
				start := min(page.Offset, page.Total)
				end := start + min(page.Limit, page.Total-start)
				if end < page.Total {
					page.NextOffset = &end
				}
				list.Items = list.Items[start:end]
			}
			assessments, err := o.assessList(r.Context(), list)
			if err != nil {
				httpapi.SendQueryError(w, err)
				return
			}
			payload := projectMeasurementList(list, assessments, page)
			if values.Get(measurementViewQuery) == measurementSummaryView {
				summary, err := queryapi.MeasurementSummaryJSON(payload, false)
				if err != nil {
					httpapi.SendQueryError(w, err)
					return
				}
				writeMeasurement(w, summary)
				return
			}
			writeMeasurement(w, payload)

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

// Result selection comes from the accepted catalog, never a condition-matched cell.
type measurementResultReader struct {
	read         func(context.Context, string, string) (queryapi.BatchEnergyOptimizationResultSnapshot, error)
	measurements *measurementService
}

func (p measurementResultReader) Read(
	ctx context.Context, namespace, name string,
) (queryapi.BatchEnergyOptimizationResultSnapshot, error) {
	snapshot, err := p.read(ctx, namespace, name)
	if err != nil || p.measurements.catalog == nil {
		return snapshot, err
	}
	read, err := p.measurements.catalog(ctx)
	if err != nil || read.Accepted == nil || read.Accepted.CurrentMeasurements == nil {
		return snapshot, nil
	}
	used := false
	id := coverageRequestID(snapshot.Campaign)
	view := adoptedCatalogView(read)
	for _, profile := range view.Profiles {
		profileID := profile.ID
		if profile.Reuse != nil {
			profileID = profile.Reuse.ConfirmationProfileID
		}
		if profileID == id && measurementProfileCurrent(view, profile) {
			used = true
			break
		}
	}
	snapshot.SchedulerUsed = &used
	return snapshot, nil
}

// Keep the native Kubernetes list and each complete CR intact; assessment is
// only an additional read-projection field.
func projectMeasurementList(
	list api.BatchEnergyOptimizationList, assessments []json.RawMessage, page *httpapi.ProfilePage,
) any {
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
		Items         []item               `json:"items"`
		Page          *httpapi.ProfilePage `json:"page,omitempty"`
	}{list.TypeMeta, list.ListMeta, items, page}
}

func measurementListPage(values url.Values) (*httpapi.ProfilePage, error) {
	page := &httpapi.ProfilePage{Limit: 25}
	for key, items := range values {
		if len(items) != 1 {
			return nil, errors.New("repeated measurement list query")
		}
		switch key {
		case measurementViewQuery:
			if items[0] != measurementSummaryView {
				return nil, errors.New("invalid measurement list view")
			}
		case measurementLimitQuery, measurementOffsetQuery:
			n, err := strconv.Atoi(items[0])
			if err != nil || strconv.Itoa(n) != items[0] ||
				key == measurementLimitQuery && (n < 1 || n > 50) ||
				key == measurementOffsetQuery && (n < 0 || n > 1000000) {
				return nil, errors.New("limit must be 1–50 and offset 0–1000000")
			}
			if key == measurementLimitQuery {
				page.Limit = n
			} else {
				page.Offset = n
			}
		default:
			return nil, errors.New("unknown measurement list query")
		}
	}
	if len(values) == 0 {
		return nil, nil
	}
	return page, nil
}
