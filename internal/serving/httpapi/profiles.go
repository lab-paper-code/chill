package httpapi

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

// ProfilesView is the catalog the scheduler derived from completed measurements:
// the latest profile per condition, its preparation and per-source issues.
type ProfilesView struct {
	ConfigVersion string `json:"configVersion"`
	servingkube.CatalogView
	Preparations map[string]servingkube.PrepareSpec `json:"preparations"`
	Page         *ProfilePage                       `json:"page,omitempty"`
	Filters      ProfileFilters                     `json:"filters"`
}

type ProfilePage struct {
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	Total      int  `json:"total"`
	NextOffset *int `json:"nextOffset,omitempty"`
}

type ProfileFilters struct {
	Models        []string `json:"models"`
	DeviceClasses []string `json:"deviceClasses"`
}

func pageProfiles(view ProfilesView, values url.Values) (ProfilesView, error) {
	limit, offset := 25, 0
	for key, value := range values {
		if len(value) != 1 || len(value[0]) > 512 {
			return ProfilesView{}, &serving.Error{Code: "InvalidQuery", Message: "invalid profile query"}
		}
		switch key {
		case "limit", "offset":
			n, err := strconv.Atoi(value[0])
			if err != nil || (key == "limit" && (n < 1 || n > 50)) ||
				(key == "offset" && (n < 0 || n > 1000000)) {
				return ProfilesView{}, &serving.Error{
					Code: "InvalidQuery", Message: "profile limit must be 1–50 and offset 0–1000000",
				}
			}
			if key == "limit" {
				limit = n
			} else {
				offset = n
			}
		case "model", "deviceClass", "profile":
		default:
			return ProfilesView{}, &serving.Error{Code: "InvalidQuery", Message: "unknown profile query"}
		}
	}
	selected := make([]serving.Profile, 0, len(view.Profiles))
	models, classes := map[string]bool{}, map[string]bool{}
	view.Filters = ProfileFilters{Models: []string{}, DeviceClasses: []string{}}
	for _, profile := range view.Profiles {
		model, class := profile.Condition.LogicalModel, profile.Condition.DeviceClass
		models[model], classes[class] = true, true
		if (values.Get("model") == "" || values.Get("model") == model) &&
			(values.Get("deviceClass") == "" || values.Get("deviceClass") == class) &&
			(values.Get("profile") == "" || values.Get("profile") == profile.ID) {
			selected = append(selected, profile)
		}
	}
	for model := range models {
		view.Filters.Models = append(view.Filters.Models, model)
	}
	for class := range classes {
		view.Filters.DeviceClasses = append(view.Filters.DeviceClasses, class)
	}
	sort.Strings(view.Filters.Models)
	sort.Strings(view.Filters.DeviceClasses)
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	start := min(offset, len(selected))
	end := min(start+limit, len(selected))
	view.Page = &ProfilePage{Offset: offset, Limit: limit, Total: len(selected)}
	if end < len(selected) {
		view.Page.NextOffset = &end
	}
	view.Profiles = selected[start:end]
	return view, nil
}

func (h *Handler) schedulerProfiles(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.Profiles == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler is not enabled"})
		return
	}
	view, err := h.config.Profiles(r.Context())
	if err != nil {
		sendError(w, err)
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil {
		view, err = pageProfiles(view, values)
	}
	if err != nil {
		sendError(w, &serving.Error{Code: "InvalidQuery", Message: "invalid profile query"})
		return
	}
	if h.config.DecorateProfiles != nil {
		projected, err := h.config.DecorateProfiles(r.Context(), view)
		if err != nil {
			sendError(w, err)
			return
		}
		sendJSON(w, http.StatusOK, projected)
		return
	}
	sendJSON(w, http.StatusOK, view)
}

// schedulerCapacity returns the capacity rows the autonomous Workloads need;
// a row still being computed is CapacityPending.
func (h *Handler) schedulerCapacity(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		sendError(w, err)
		return
	}
	if h.config.Scheduler == nil {
		sendError(w, &serving.Error{Code: "SchedulerUnavailable", Message: "scheduler is not enabled"})
		return
	}
	sendJSON(w, http.StatusOK, struct {
		Rows []serving.CapacityRow `json:"rows"`
	}{h.config.Scheduler.Capacity()})
}
