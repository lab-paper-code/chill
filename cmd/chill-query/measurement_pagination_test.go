package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func measurementPaginationHandler(t *testing.T) *http.ServeMux {
	t.Helper()
	objects := make([]client.Object, 61)
	for i := range objects {
		objects[i] = &api.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{
			Namespace: "allowed", Name: fmt.Sprintf("request-%03d", i), UID: "retained-uid",
		}}
	}
	service := &measurementService{
		client: fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(objects...).Build(),
		opts:   options{profileNamespaces: []string{"allowed"}},
	}
	mux := http.NewServeMux()
	service.register(mux)
	return mux
}

func TestMeasurementListPagesPreserveAllNativeRequests(t *testing.T) {
	mux := measurementPaginationHandler(t)
	const path = "/api/v1/namespaces/allowed/batch-energy-optimizations"
	for _, offset := range []int{0, 25, 50, 61, 100} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s?limit=25&offset=%d", path, offset), nil))
		var page struct {
			Items []api.BatchEnergyOptimization `json:"items"`
			Page  httpapi.ProfilePage           `json:"page"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK {
			t.Fatalf("page %d: status=%d body=%s error=%v", offset, response.Code, response.Body.String(), err)
		}
		want := min(25, 61-min(offset, 61))
		if len(page.Items) != want || page.Page.Total != 61 || page.Page.Offset != offset || page.Page.Limit != 25 {
			t.Fatalf("page %d: %+v", offset, page)
		}
		for i, item := range page.Items {
			if item.Name != fmt.Sprintf("request-%03d", offset+i) || item.UID != "retained-uid" {
				t.Fatalf("lost request identity: %+v", item)
			}
		}
		if offset+want < 61 {
			if page.Page.NextOffset == nil || *page.Page.NextOffset != offset+want {
				t.Fatal("next page missing")
			}
		} else if page.Page.NextOffset != nil {
			t.Fatal("invented next page")
		}
	}
	for _, query := range []string{
		"limit=0", "limit=51", "offset=-1", "offset=1000001", "offset=1&offset=2", "view=full", "unknown=x",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid query %s: %d", query, response.Code)
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	var native api.BatchEnergyOptimizationList
	if err := json.Unmarshal(response.Body.Bytes(), &native); err != nil || len(native.Items) != 61 {
		t.Fatal("unpaged native read changed")
	}
}

func TestMeasurementSummaryWithoutExplicitPaginationPreservesAllRequests(t *testing.T) {
	mux := measurementPaginationHandler(t)
	const path = "/api/v1/namespaces/allowed/batch-energy-optimizations"
	for _, version := range []string{"v1", "v2"} {
		response := httptest.NewRecorder()
		query := "?view=summary"
		if version == "v2" {
			query += "&summary=v2"
		}
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+query, nil))
		var root map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &root); err != nil || response.Code != http.StatusOK {
			t.Fatalf("summary %s: %d %s", version, response.Code, response.Body.String())
		}
		if _, paged := root["page"]; paged {
			t.Fatal("implicit summary pagination truncates old consumers")
		}
		var projection struct {
			Version string            `json:"version"`
			Values  []json.RawMessage `json:"values"`
		}
		if err := json.Unmarshal(root["readProjection"], &projection); err != nil ||
			projection.Version != "measurement-summary."+version {
			t.Fatalf("wrong negotiated version: %+v", projection)
		}
		var reference string
		if err := json.Unmarshal(root["items"], &reference); err != nil {
			t.Fatal(err)
		}
		index, err := strconv.Atoi(reference[1:])
		if err != nil {
			t.Fatal(err)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(projection.Values[index], &items); err != nil || len(items) != 61 {
			t.Fatal("unpaged summary lost later requests")
		}
	}
}
