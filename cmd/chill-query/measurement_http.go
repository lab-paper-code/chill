package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"

	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/queryapi/readapi"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/client-go/rest"
)

const (
	measurementSummaryView = "summary"
	measurementViewQuery   = "view"
)

func (o *measurementService) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/scheduler/profiles", func(w http.ResponseWriter, r *http.Request) {
		read, err := o.catalog(r.Context())
		if err != nil {
			httpapi.SendQueryError(w, err)
			return
		}
		view := adoptedCatalogView(read)
		queryView := queryCoverageCatalog(view)
		view.DisplayStatus = func(p serving.Profile) string {
			a, _ := queryapi.AssessCatalogProfile(queryView, queryCoverageProfile(p))
			return a.DisplayStatus
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		sharePaths := false
		if err == nil {
			sharePaths, err = readapi.MeasurementSummaryVersion(values)
		}
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
			summary, err := queryapi.MeasurementSummaryJSON(payload, true, sharePaths)
			if err != nil {
				httpapi.SendQueryError(w, err)
				return
			}
			readapi.WriteMeasurement(w, summary)
			return
		}
		readapi.WriteMeasurement(w, payload)
	})
	mux.HandleFunc(
		"GET /api/v1/namespaces/{namespace}/measurement-coverage",
		func(w http.ResponseWriter, r *http.Request) {
			values, err := url.ParseQuery(r.URL.RawQuery)
			sharePaths := false
			if err == nil {
				sharePaths, err = readapi.MeasurementSummaryVersion(values)
			}
			var query httpapi.MeasurementCoverageQuery
			if err == nil {
				query, err = httpapi.ParseMeasurementCoverageQuery(values)
			}
			if err != nil {
				httpapi.SendQueryError(w, &serving.Error{Code: "InvalidCoverageQuery", Message: "유효한 query가 필요합니다"})
				return
			}
			payload, err := o.coverage.Coverage(r.Context(), r.PathValue("namespace"),
				queryapi.CoverageQuery{Batches: query.Batches, Node: query.Node, Summary: query.Summary}, sharePaths)
			if err != nil {
				var domain *queryapi.MeasurementError
				if errors.As(err, &domain) {
					if domain.Code == "CoverageWarming" {
						w.Header().Set("Retry-After", "2")
					}
					err = &serving.Error{Code: domain.Code, Message: domain.Message}
				}
				httpapi.SendQueryError(w, err)
				return
			}
			readapi.WriteMeasurement(w, payload)
		},
	)
	mux.HandleFunc(
		"GET /api/v1/namespaces/{namespace}/batch-energy-optimizations",
		func(w http.ResponseWriter, r *http.Request) {
			values, err := url.ParseQuery(r.URL.RawQuery)
			sharePaths := false
			if err == nil {
				sharePaths, err = readapi.MeasurementSummaryVersion(values)
			}
			var page *readapi.Page
			if err == nil {
				page, err = readapi.MeasurementListPage(values)
			}
			if err != nil {
				httpapi.SendQueryError(
					w,
					&serving.Error{Code: "InvalidQuery", Message: "invalid measurement list query"},
				)
				return
			}
			list, err := querykube.ListMeasurementRequests(r.Context(), o.client, r.PathValue("namespace"), o.namespace)
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
			assessments, err := querykube.AssessMeasurementRequests(
				r.Context(), queryCatalogSource(o.catalog), list,
			)
			if err != nil {
				httpapi.SendQueryError(w, err)
				return
			}
			payload := readapi.ProjectMeasurementList(list, assessments, page)
			if values.Get(measurementViewQuery) == measurementSummaryView {
				summary, err := queryapi.MeasurementSummaryJSON(payload, false, sharePaths)
				if err != nil {
					httpapi.SendQueryError(w, err)
					return
				}
				readapi.WriteMeasurement(w, summary)
				return
			}
			readapi.WriteMeasurement(w, payload)

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
					readapi.WriteMeasurement(w, value)
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
