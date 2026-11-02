package httpapi

import (
	"net/url"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestProfilePagingFiltersBeforeSlicingWithoutMutatingCatalog(t *testing.T) {
	view := ProfilesView{}
	for _, id := range []string{"c", "b", "a"} {
		p := serving.Profile{ID: id}
		p.Condition.LogicalModel, p.Condition.DeviceClass = "model", "device"
		if id == "a" {
			p.Condition.LogicalModel = "other"
		}
		view.Profiles = append(view.Profiles, p)
	}
	got, err := PageProfiles(view, url.Values{"model": {"model"}, "deviceClass": {"device"}, "limit": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Page.Total != 2 || got.Page.NextOffset == nil || *got.Page.NextOffset != 1 || len(got.Profiles) != 1 || got.Profiles[0].ID != "b" || len(got.Filters.Models) != 2 {
		t.Fatalf("filtered page: %+v", got)
	}
	if view.Profiles[0].ID != "c" {
		t.Fatal("query reordered shared scheduler catalog")
	}
	got, err = PageProfiles(view, url.Values{"profile": {"c"}})
	if err != nil || len(got.Profiles) != 1 || got.Profiles[0].ID != "c" {
		t.Fatalf("exact profile: %+v %v", got, err)
	}
	for _, values := range []url.Values{{"limit": {"51"}}, {"offset": {"-1"}}, {"model": {"a", "b"}}, {"unexpected": {"x"}}} {
		if _, err := PageProfiles(view, values); err == nil {
			t.Fatalf("invalid query accepted: %v", values)
		}
	}
}

func TestProfileSortAndEveryFilterPrecedePagination(t *testing.T) {
	view := ProfilesView{DisplayStatus: func(profile serving.Profile) string {
		if profile.ID == "z" {
			return "usable"
		}
		return "problem"
	}}
	for _, id := range []string{"z", "b", "a"} {
		value, seconds := "a", int64(1)
		if id == "z" {
			value, seconds = "z", 2
		}
		p := serving.Profile{ID: id, NodeName: value, MeasuredAt: time.Unix(seconds, 0)}
		p.Condition.LogicalModel, p.Condition.DeviceClass = value, value
		p.Condition.PowerMode, p.Condition.Artifact.ExecutionPath = value, value
		view.Profiles = append(view.Profiles, p)
	}
	for _, key := range []string{"model", "deviceClass", "node", "powerMode", "path", "created", "status"} {
		for _, order := range []string{"asc", "desc"} {
			want := "b"
			if order == "desc" {
				want = "a"
			}
			got, err := PageProfiles(view, url.Values{"sort": {key}, "order": {order}, "limit": {"1"}, "offset": {"1"}})
			if err != nil || len(got.Profiles) != 1 || got.Profiles[0].ID != want || got.Page.Total != 3 {
				t.Fatalf("%s %s: %+v %v", key, order, got, err)
			}
		}
	}
	for key, value := range map[string]string{"q": "z", "model": "z", "deviceClass": "z", "node": "z", "powerMode": "z", "path": "z", "status": "usable"} {
		got, err := PageProfiles(view, url.Values{key: {value}, "limit": {"1"}})
		if err != nil || len(got.Profiles) != 1 || got.Profiles[0].ID != "z" || got.Page.Total != 1 || len(got.Filters.Nodes) != 2 || len(got.Filters.Statuses) != 2 {
			t.Fatalf("%s global filter: %+v %v", key, got, err)
		}
	}
	for _, query := range []string{"cpu=2", "sort=cpu", "sort=unknown", "sort=", "sort=class", "sort=model&sort=model", "sort=model&order=DESC", "sort=model&order=", "sort=model&order=asc&order=desc", "order=asc"} {
		values, _ := url.ParseQuery(query)
		if _, err := PageProfiles(view, values); err == nil {
			t.Fatalf("invalid sort accepted: %s", query)
		}
	}
	if view.Profiles[0].ID != "z" {
		t.Fatal("sort mutated shared catalog")
	}
}

func TestProfileSummaryPreservesSelectionAndPaging(t *testing.T) {
	view := ProfilesView{}
	view.Profiles = []serving.Profile{{ID: "b"}, {ID: "a"}}
	got, err := PageProfiles(view, url.Values{"view": {"summary"}, "limit": {"1"}, "offset": {"1"}})
	if err != nil || got.Page.Total != 2 || got.Page.Offset != 1 || len(got.Profiles) != 1 || got.Profiles[0].ID != "b" {
		t.Fatalf("summary page lost selection: %+v %v", got, err)
	}
	for _, values := range []url.Values{{"view": {""}}, {"view": {"compact"}}, {"view": {"full"}}, {"view": {"summary", "summary"}}} {
		if _, err := PageProfiles(view, values); err == nil {
			t.Fatalf("invalid summary query accepted: %v", values)
		}
	}
}
