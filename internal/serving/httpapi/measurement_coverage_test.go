package httpapi

import (
	"net/url"
	"testing"
)

func TestMeasurementCoverageStrictQuery(t *testing.T) {
	for _, raw := range []string{
		"batches=2,1", "batches=1,1", "batches=0", "batches=01", "batches=",
		"node=", "node=a&node=b", "model=a", "batches=2147483648",
	} {
		values, _ := url.ParseQuery(raw)
		if _, err := ParseMeasurementCoverageQuery(values); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	values, _ := url.ParseQuery("batches=1,2,4&node=node-a")
	query, err := ParseMeasurementCoverageQuery(values)
	if err != nil || len(query.Batches) != 3 || query.Node != "node-a" {
		t.Fatalf("%+v %v", query, err)
	}
}
