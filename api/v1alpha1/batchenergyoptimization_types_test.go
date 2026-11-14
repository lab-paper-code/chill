package v1alpha1_test

import (
	"reflect"
	"strings"
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

// Go before 1.24 ignores the omitzero tag option, and the images build with
// such a toolchain, so a typed Create sends every zero-valued field it guards.
func TestBatchEnergyOptimizationSchemaHasNoOmitzero(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(path string, typ reflect.Type)
	walk = func(path string, typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] || typ.PkgPath() != reflect.TypeOf(edge.BatchEnergyOptimization{}).PkgPath() {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			for _, option := range strings.Split(field.Tag.Get("json"), ",")[1:] {
				if option == "omitzero" {
					t.Errorf("%s.%s uses omitzero", path, field.Name)
				}
			}
			walk(path+"."+field.Name, field.Type)
		}
	}
	walk("BatchEnergyOptimization", reflect.TypeOf(edge.BatchEnergyOptimization{}))
}
