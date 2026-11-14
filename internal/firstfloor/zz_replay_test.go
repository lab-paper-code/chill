package firstfloor

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestZZReplayTable(t *testing.T) {
	raw, err := os.ReadFile("/tmp/LaneQuantile/genesis2-ic-b1.json")
	if err != nil {
		t.Skip(err)
	}
	var trials map[string][]ServiceSample
	if err := json.Unmarshal(raw, &trials); err != nil {
		t.Fatal(err)
	}
	gaps := []time.Duration{100 * time.Microsecond, 5 * time.Millisecond, 50 * time.Millisecond, 2 * time.Second}
	for _, limit := range []int{500, 0} {
		var samples []ServiceSample
		for _, records := range trials {
			if limit > 0 {
				records = records[:min(limit, len(records))]
			}
			samples = append(samples, records...)
		}
		d, err := NewServiceDistribution(samples)
		if err != nil {
			t.Fatal(err)
		}
		var row strings.Builder
		fmt.Fprintf(&row, "records/trial=%d pooled=%d dropped=%d:", limit, len(d.gaps), d.DroppedFirstExecutions())
		for _, percentile := range []float64{95, 99} {
			for _, g := range gaps {
				gap := int64(g)
				q, s, ok := d.QuantileNanos(&gap, percentile)
				fmt.Fprintf(&row, " p%g@%v=%.1fms(n=%d,dist=%v,ok=%v)", percentile, g, float64(q)/1e6, s.Records, time.Duration(s.DistanceNanos), ok)
			}
		}
		t.Log(row.String())
	}
}
