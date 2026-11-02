package httpapi

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/util/validation"
)

// MeasurementCoverageQuery is transport-owned; the composition root connects
// this HTTP contract to the shared assessment projection.
type MeasurementCoverageQuery struct {
	Batches []int
	Node    string
}

func ParseMeasurementCoverageQuery(values url.Values) (MeasurementCoverageQuery, error) {
	query := MeasurementCoverageQuery{Batches: []int{}}
	invalid := &serving.Error{
		Code:    "InvalidCoverageQuery",
		Message: "batches는 중복 없는 오름차순 양의 정수, node는 정확한 이름이어야 합니다",
	}
	for key, items := range values {
		if (key != "batches" && key != queryNode) || len(items) != 1 || items[0] == "" {
			return query, invalid
		}
		if key == queryNode {
			if len(validation.IsDNS1123Subdomain(items[0])) != 0 {
				return query, invalid
			}
			query.Node = items[0]
			continue
		}
		previous := 0
		for _, raw := range strings.Split(items[0], ",") {
			batch, err := strconv.Atoi(raw)
			if err != nil || batch <= previous || strconv.Itoa(batch) != raw || batch > 2147483647 {
				return query, invalid
			}
			query.Batches = append(query.Batches, batch)
			previous = batch
		}
	}
	return query, nil
}
