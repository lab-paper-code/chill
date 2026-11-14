package v1alpha1_test

import (
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestSessionConfidenceUsesRepresentableProbability(t *testing.T) {
	for _, value := range []string{"0.5", "0.95", "9.5e-1"} {
		got, err := (edge.BatchEnergyOptimizationSessionSpec{ConfidenceLevel: value}).Confidence()
		if err != nil || got < .5 || got >= 1 {
			t.Fatalf("confidence %q = %g, %v", value, got, err)
		}
	}
	for _, value := range []string{"0.999999999999999999999999", "0.49", "1", "NaN", "+Inf"} {
		if _, err := (edge.BatchEnergyOptimizationSessionSpec{ConfidenceLevel: value}).Confidence(); err == nil {
			t.Fatalf("invalid confidence accepted: %q", value)
		}
	}
}
