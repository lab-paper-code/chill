package readapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	measurementSummaryView = "summary"
	measurementViewQuery   = "view"
	measurementLimitQuery  = "limit"
	measurementOffsetQuery = "offset"
)

func WriteMeasurement(w http.ResponseWriter, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if raw, ok := value.(json.RawMessage); ok {
		_, _ = w.Write(raw)
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

// Keep the native Kubernetes list and each complete CR intact; assessment is
// only an additional read-projection field.
func ProjectMeasurementList(
	list api.BatchEnergyOptimizationList, assessments []json.RawMessage, page *Page,
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
		Items         []item `json:"items"`
		Page          *Page  `json:"page,omitempty"`
	}{list.TypeMeta, list.ListMeta, items, page}
}

func MeasurementListPage(values url.Values) (*Page, error) {
	page := &Page{Limit: 25}
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
	if !values.Has(measurementLimitQuery) && !values.Has(measurementOffsetQuery) {
		return nil, nil
	}
	return page, nil
}

func MeasurementSummaryVersion(values url.Values) (bool, error) {
	if !values.Has("summary") {
		return false, nil
	}
	if len(values["summary"]) != 1 || values.Get("summary") != "v2" ||
		len(values[measurementViewQuery]) != 1 || values.Get(measurementViewQuery) != measurementSummaryView {
		return false, errors.New("summary=v2 requires view=summary")
	}
	values.Del("summary")
	return true, nil
}
