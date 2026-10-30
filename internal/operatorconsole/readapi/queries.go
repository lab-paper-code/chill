package readapi

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// QueryReaders reuse the same read-through snapshots as the standalone CLI.
// A missing optional query is unavailable, never an empty successful listing.
type QueryReaders struct {
	Systems                  func(context.Context) (operatorconsole.SystemSnapshot, error)
	Inventory                func(context.Context) (operatorconsole.InventorySnapshot, error)
	ProfilingRuns            func(context.Context, string) (operatorconsole.ProfilingRunsSnapshot, error)
	ModelCompatibility       func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error)
	ModelCompatibilityMatrix func(context.Context) (operatorconsole.ModelCompatibilityMatrixView, error)
}

type queryPage struct {
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	Total      int  `json:"total"`
	NextOffset *int `json:"nextOffset,omitempty"`
}

type listQuery struct {
	limit, offset     int
	search, condition string
	sort, order       string
	filters           map[string]string
}

const queryNode = "node"

func parseListQuery(r *http.Request, runs bool) (listQuery, bool) {
	q := listQuery{limit: 50, filters: map[string]string{}}
	if len(r.URL.RawQuery) > 4096 {
		return q, false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, false
	}
	for key, value := range values {
		if len(value) != 1 {
			return q, false
		}
		switch key {
		case "limit":
			limit, err := strconv.Atoi(value[0])
			if err != nil || limit <= 0 || limit > 200 {
				return q, false
			}
			q.limit = limit
		case "offset":
			offset, err := strconv.Atoi(value[0])
			if err != nil || offset < 0 || offset > 1000000 {
				return q, false
			}
			q.offset = offset
		case "q":
			if len(value[0]) > 256 {
				return q, false
			}
			q.search = strings.ToLower(value[0])
		case "sort":
			if !runs || !slices.Contains([]string{"name", "created", "model", "powerMode", "status"}, value[0]) {
				return q, false
			}
			q.sort = value[0]
		case "order":
			if !runs || (value[0] != "asc" && value[0] != "desc") {
				return q, false
			}
			q.order = value[0]
		case "condition":
			kind, status, ok := strings.Cut(value[0], "=")
			if !runs || !ok || kind == "" || len(value[0]) > 128 ||
				(status != string(metav1.ConditionTrue) &&
					status != string(metav1.ConditionFalse) &&
					status != string(metav1.ConditionUnknown)) {
				return q, false
			}
			q.condition = value[0]
		case "model", "deviceClass", queryNode, "path", "powerMode", "cpu", "status", "parentUID", "group":
			if !runs || len(value[0]) > 2048 {
				return q, false
			}
			q.filters[key] = value[0]
			if key == "group" && value[0] != "" {
				group, valid := canonicalRunGroup(value[0])
				if !valid {
					return q, false
				}
				q.filters[key] = group
			}
		default:
			return q, false
		}
	}
	if q.order != "" && q.sort == "" {
		return q, false
	}
	return q, true
}

func pageBounds(total int, q listQuery) (int, int, queryPage) {
	start := min(q.offset, total)
	end := min(start+q.limit, total)
	page := queryPage{Offset: q.offset, Limit: q.limit, Total: total}
	if end < total {
		page.NextOffset = &end
	}
	return start, end, page
}

func matchesCondition(conditions []metav1.Condition, filter string) bool {
	if filter == "" {
		return true
	}
	kind, status, _ := strings.Cut(filter, "=")
	for _, condition := range conditions {
		if condition.Type == kind && string(condition.Status) == status {
			return true
		}
	}
	return false
}

// NewQueryHandler exposes existing pure projections; deployment owns auth.
func NewQueryHandler(readers QueryReaders) http.Handler {
	mux := http.NewServeMux()
	RegisterModelCompatibilityQuery(mux, readers.ModelCompatibility)
	registerModelCompatibilityMatrix(mux, readers)
	mux.HandleFunc("GET /api/v1/systems", func(w http.ResponseWriter, r *http.Request) {
		q, valid := parseListQuery(r, false)
		if !valid {
			writeError(w, http.StatusBadRequest, "InvalidQuery")
			return
		}
		if readers.Systems == nil {
			writeError(w, http.StatusServiceUnavailable, "SystemsUnavailable")
			return
		}
		snapshot, err := readers.Systems(r.Context())
		if err != nil {
			writeError(w, queryStatus(err), "SystemsUnavailable")
			return
		}
		selected := snapshot.Systems[:0]
		for _, system := range snapshot.Systems {
			if strings.Contains(strings.ToLower(system.Name), q.search) {
				selected = append(selected, system)
			}
		}
		start, end, page := pageBounds(len(selected), q)
		snapshot.Systems = selected[start:end]
		writeJSON(w, struct {
			operatorconsole.SystemsView
			Page queryPage `json:"page"`
		}{operatorconsole.BuildSystems(snapshot), page})
	})
	mux.HandleFunc("GET /api/v1/inventory", func(w http.ResponseWriter, r *http.Request) {
		q, valid := parseListQuery(r, false)
		if !valid {
			writeError(w, http.StatusBadRequest, "InvalidQuery")
			return
		}
		if readers.Inventory == nil {
			writeError(w, http.StatusServiceUnavailable, "InventoryUnavailable")
			return
		}
		snapshot, err := readers.Inventory(r.Context())
		if err != nil {
			writeError(w, queryStatus(err), "InventoryUnavailable")
			return
		}
		classes := snapshot.Classes[:0]
		for _, class := range snapshot.Classes {
			if strings.Contains(strings.ToLower(class.Name), q.search) {
				classes = append(classes, class)
			}
		}
		models := snapshot.Models[:0]
		for _, model := range snapshot.Models {
			if strings.Contains(strings.ToLower(model.Name), q.search) {
				models = append(models, model)
			}
		}
		classStart, classEnd, classPage := pageBounds(len(classes), q)
		modelStart, modelEnd, modelPage := pageBounds(len(models), q)
		snapshot.Classes, snapshot.Models = classes[classStart:classEnd], models[modelStart:modelEnd]
		writeJSON(w, struct {
			operatorconsole.InventoryView
			ClassPage queryPage `json:"classPage"`
			ModelPage queryPage `json:"modelPage"`
		}{operatorconsole.BuildInventory(snapshot), classPage, modelPage})
	})
	mux.HandleFunc("GET /api/v1/namespaces/{namespace}/profiling-runs", func(w http.ResponseWriter, r *http.Request) {
		namespace := r.PathValue("namespace")
		q, valid := parseListQuery(r, true)
		if !valid || len(validation.IsDNS1123Label(namespace)) != 0 {
			writeError(w, http.StatusBadRequest, "InvalidQuery")
			return
		}
		if readers.ProfilingRuns == nil {
			writeError(w, http.StatusServiceUnavailable, "ProfilingRunsUnavailable")
			return
		}
		snapshot, err := readers.ProfilingRuns(r.Context(), namespace)
		if err != nil {
			writeError(w, queryStatus(err), "ProfilingRunsUnavailable")
			return
		}
		selected := make([]api.ProfilingRun, 0, len(snapshot.Runs))
		for _, run := range snapshot.Runs {
			if run.Namespace == namespace && strings.Contains(strings.ToLower(run.Name), q.search) &&
				matchesCondition(run.Status.Conditions, q.condition) && matchesRunFilters(run, q.filters) {
				selected = append(selected, run)
			}
		}
		sortRuns(selected, q)
		start, end, page := pageBounds(len(selected), q)
		snapshot.Namespace, snapshot.Runs = namespace, selected[start:end]
		writeJSON(w, struct {
			operatorconsole.ProfilingRunsView
			Page queryPage `json:"page"`
		}{operatorconsole.BuildProfilingRuns(snapshot), page})
	})
	return mux
}
