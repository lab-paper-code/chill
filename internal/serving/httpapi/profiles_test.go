package httpapi

import (
	"net/url"
	"testing"

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
