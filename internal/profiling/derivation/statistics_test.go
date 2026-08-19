package profilederivation

import (
	"math"
	"testing"
)

func TestStudentT95UsesExactCriticalValueBeyondLegacyTable(t *testing.T) {
	const want = 2.0395134463964077
	if got := studentTCritical(0.95, 31); math.Abs(got-want) > 1e-12 {
		t.Fatalf("studentTCritical(0.95, 31) = %.15f, want %.15f", got, want)
	}
}

func TestPercentileUsesEmpiricalNearestRank(t *testing.T) {
	values := []float64{4, 1, 3, 2}
	for _, test := range []struct {
		quantile float64
		want     float64
	}{
		{quantile: 0, want: 1},
		{quantile: 0.50, want: 2},
		{quantile: 0.75, want: 3},
		{quantile: 1, want: 4},
	} {
		if got := percentile(values, test.quantile); got != test.want {
			t.Errorf("percentile(%v, %.2f) = %v, want %v", values, test.quantile, got, test.want)
		}
	}
}
