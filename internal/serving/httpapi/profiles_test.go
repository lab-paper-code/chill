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
	got, err := pageProfiles(view, url.Values{"model": {"model"}, "deviceClass": {"device"}, "limit": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Page.Total != 2 || got.Page.NextOffset == nil || *got.Page.NextOffset != 1 || len(got.Profiles) != 1 || got.Profiles[0].ID != "b" || len(got.Filters.Models) != 2 {
		t.Fatalf("filtered page: %+v", got)
	}
	if view.Profiles[0].ID != "c" {
		t.Fatal("query reordered shared scheduler catalog")
	}
	got, err = pageProfiles(view, url.Values{"profile": {"c"}})
	if err != nil || len(got.Profiles) != 1 || got.Profiles[0].ID != "c" {
		t.Fatalf("exact profile: %+v %v", got, err)
	}
	for _, values := range []url.Values{{"limit": {"51"}}, {"offset": {"-1"}}, {"model": {"a", "b"}}, {"unexpected": {"x"}}} {
		if _, err := pageProfiles(view, values); err == nil {
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
		value, cpu, seconds := "a", "2", int64(1)
		if id == "z" {
			value, cpu, seconds = "z", "10", 2
		}
		p := serving.Profile{ID: id, NodeName: value, MeasuredAt: time.Unix(seconds, 0)}
		p.Condition.LogicalModel, p.Condition.DeviceClass = value, value
		p.Condition.PowerMode, p.Condition.Artifact.ExecutionPath, p.Condition.CPU.Request = value, value, cpu
		view.Profiles = append(view.Profiles, p)
	}
	for _, key := range []string{"model", "deviceClass", "node", "powerMode", "path", "cpu", "created", "status"} {
		for _, order := range []string{"asc", "desc"} {
			want := "b"
			if order == "desc" {
				want = "a"
			}
			got, err := pageProfiles(view, url.Values{"sort": {key}, "order": {order}, "limit": {"1"}, "offset": {"1"}})
			if err != nil || len(got.Profiles) != 1 || got.Profiles[0].ID != want || got.Page.Total != 3 {
				t.Fatalf("%s %s: %+v %v", key, order, got, err)
			}
		}
	}
	for key, value := range map[string]string{"q": "z", "model": "z", "deviceClass": "z", "node": "z", "powerMode": "z", "path": "z", "cpu": "10", "status": "usable"} {
		got, err := pageProfiles(view, url.Values{key: {value}, "limit": {"1"}})
		if err != nil || len(got.Profiles) != 1 || got.Profiles[0].ID != "z" || got.Page.Total != 1 || len(got.Filters.Nodes) != 2 || len(got.Filters.Statuses) != 2 {
			t.Fatalf("%s global filter: %+v %v", key, got, err)
		}
	}
	for _, query := range []string{"sort=unknown", "sort=", "sort=class", "sort=model&sort=model", "sort=model&order=DESC", "sort=model&order=", "sort=model&order=asc&order=desc", "order=asc"} {
		values, _ := url.ParseQuery(query)
		if _, err := pageProfiles(view, values); err == nil {
			t.Fatalf("invalid sort accepted: %s", query)
		}
	}
	if view.Profiles[0].ID != "z" {
		t.Fatal("sort mutated shared catalog")
	}
}
