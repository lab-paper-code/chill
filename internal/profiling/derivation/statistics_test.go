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
