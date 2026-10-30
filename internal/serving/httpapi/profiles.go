package httpapi

import (
	"cmp"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ProfilesView is the catalog the scheduler derived from completed measurements:
// the latest profile per condition, its preparation and per-source issues.
type ProfilesView struct {
	ConfigVersion string `json:"configVersion"`
	servingkube.CatalogView
	Preparations  map[string]servingkube.PrepareSpec `json:"preparations"`
	Page          *ProfilePage                       `json:"page,omitempty"`
	Filters       ProfileFilters                     `json:"filters"`
	DisplayStatus func(serving.Profile) string       `json:"-"`
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
	Nodes         []string `json:"nodes"`
	PowerModes    []string `json:"powerModes"`
	Paths         []string `json:"paths"`
	CPUs          []string `json:"cpus"`
	Statuses      []string `json:"statuses"`
}

const (
	profileLimitQuery       = "limit"
	profileModelQuery       = "model"
	profileCPUQuery         = "cpu"
	queryNode               = "node"
	profileOrderDesc        = "desc"
	profilePathQuery        = "path"
	profileStatusQuery      = "status"
	profileDeviceClassQuery = "deviceClass"
	profilePowerModeQuery   = "powerMode"
	profileSortQuery        = "sort"
	profileOrderQuery       = "order"
	profileCreatedQuery     = "created"
	profileOffsetQuery      = "offset"
)

func parseProfileQuery(values url.Values) (int, int, error) {
	limit, offset := 25, 0
	for key, value := range values {
		if len(value) != 1 || len(value[0]) > 512 {
			return 0, 0, &serving.Error{Code: "InvalidQuery", Message: "invalid profile query"}
		}
		switch key {
		case profileLimitQuery, profileOffsetQuery:
			n, err := strconv.Atoi(value[0])
			if err != nil || (key == profileLimitQuery && (n < 1 || n > 50)) ||
				(key == profileOffsetQuery && (n < 0 || n > 1000000)) {
				return 0, 0, &serving.Error{
					Code: "InvalidQuery", Message: "profile limit must be 1–50 and offset 0–1000000",
				}
			}
			if key == profileLimitQuery {
				limit = n
			} else {
				offset = n
			}
		case profileModelQuery, profileDeviceClassQuery, "profile", "q", queryNode, profilePowerModeQuery, profilePathQuery, profileCPUQuery, profileStatusQuery:
		case profileSortQuery:
			switch value[0] {
			case profileModelQuery, profileDeviceClassQuery, queryNode, profilePowerModeQuery, profilePathQuery, profileCPUQuery, profileCreatedQuery, profileStatusQuery:
			default:
				return 0, 0, &serving.Error{Code: "InvalidQuery", Message: "unknown profile sort"}
			}
		case profileOrderQuery:
			if value[0] != "asc" && value[0] != profileOrderDesc {
				return 0, 0, &serving.Error{Code: "InvalidQuery", Message: "invalid profile order"}
			}
		default:
			return 0, 0, &serving.Error{Code: "InvalidQuery", Message: "unknown profile query"}
		}
	}
	if values.Has(profileOrderQuery) && !values.Has(profileSortQuery) {
		return 0, 0, &serving.Error{Code: "InvalidQuery", Message: "profile order requires sort"}
	}
	return limit, offset, nil
}

func pageProfiles(view ProfilesView, values url.Values) (ProfilesView, error) {
	limit, offset, err := parseProfileQuery(values)
	if err != nil {
		return ProfilesView{}, err
	}
	selected := make([]serving.Profile, 0, len(view.Profiles))
	options := map[string]map[string]bool{}
	for _, key := range []string{
		profileModelQuery, profileDeviceClassQuery, queryNode, profilePowerModeQuery, profilePathQuery, profileCPUQuery, profileStatusQuery,
	} {
		options[key] = map[string]bool{}
	}
	statuses := map[string]string{}
	cpus := map[string]resource.Quantity{}
	for _, profile := range view.Profiles {
		if values.Get(profileSortQuery) == profileCPUQuery {
			quantity, err := resource.ParseQuantity(profile.Condition.CPU.Request)
			if err != nil {
				return ProfilesView{}, &serving.Error{Code: "InvalidQuery", Message: "profile CPU quantity unavailable"}
			}
			cpus[profile.ID] = quantity
		}
		if view.DisplayStatus != nil {
			statuses[profile.ID] = view.DisplayStatus(profile)
		}
		matches := values.Get("profile") == "" || values.Get("profile") == profile.ID
		searchable := profile.ID
		for _, key := range []string{
			profileModelQuery, profileDeviceClassQuery, queryNode, profilePowerModeQuery, profilePathQuery, profileCPUQuery, profileStatusQuery,
		} {
			value := profileField(profile, statuses[profile.ID], key)
			options[key][value] = true
			matches = matches && (values.Get(key) == "" || values.Get(key) == value)
			searchable += "\n" + value
		}
		if matches && strings.Contains(strings.ToLower(searchable), strings.ToLower(values.Get("q"))) {
			selected = append(selected, profile)
		}
	}
	view.Filters = ProfileFilters{}
	for key, target := range map[string]*[]string{
		profileModelQuery: &view.Filters.Models, profileDeviceClassQuery: &view.Filters.DeviceClasses,
		queryNode: &view.Filters.Nodes, profilePowerModeQuery: &view.Filters.PowerModes,
		profilePathQuery: &view.Filters.Paths, profileCPUQuery: &view.Filters.CPUs, profileStatusQuery: &view.Filters.Statuses,
	} {
		*target = make([]string, 0, len(options[key]))
		for value := range options[key] {
			if value != "" {
				*target = append(*target, value)
			}
		}
		sort.Strings(*target)
	}
	sort.Slice(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		result := 0
		if values.Get(profileSortQuery) == profileCreatedQuery {
			result = a.MeasuredAt.Compare(b.MeasuredAt)
		} else if values.Get(profileSortQuery) == profileCPUQuery {
			left, right := cpus[a.ID], cpus[b.ID]
			result = left.Cmp(right)
		} else if key := values.Get(profileSortQuery); key != "" {
			result = strings.Compare(profileField(a, statuses[a.ID], key), profileField(b, statuses[b.ID], key))
		}
		if values.Get(profileOrderQuery) == profileOrderDesc {
			result = -result
		}
		return cmp.Or(result, strings.Compare(a.ID, b.ID)) < 0
	})
	start := min(offset, len(selected))
	end := min(start+limit, len(selected))
	view.Page = &ProfilePage{Offset: offset, Limit: limit, Total: len(selected)}
	if end < len(selected) {
		view.Page.NextOffset = &end
	}
	view.Profiles = selected[start:end]
	return view, nil
}

func profileField(profile serving.Profile, status, key string) string {
	switch key {
	case profileModelQuery:
		return profile.Condition.LogicalModel
	case profileDeviceClassQuery:
		return profile.Condition.DeviceClass
	case queryNode:
		return profile.NodeName
	case profilePowerModeQuery:
		return profile.Condition.PowerMode
	case profilePathQuery:
		return profile.Condition.Artifact.ExecutionPath
	case profileCPUQuery:
		return profile.Condition.CPU.Request
	case profileStatusQuery:
		return status
	}
	return ""
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
