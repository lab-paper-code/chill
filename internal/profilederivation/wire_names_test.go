package profilederivation

import (
	"reflect"
	"strings"
	"testing"
)

func TestEnergyWireNamesMatchGoFieldNames(t *testing.T) {
	types := []any{
		AdmittedTrial{},
		IdleAdmissionResult{},
		IdleTrial{},
		IdleBaseline{},
		Point{},
		SequentialBatchMean{},
		SequentialResult{},
	}
	for _, value := range types {
		typ := reflect.TypeOf(value)
		t.Run(typ.Name(), func(t *testing.T) {
			for index := range typ.NumField() {
				field := typ.Field(index)
				wireName := strings.Split(field.Tag.Get("json"), ",")[0]
				if wireName == "" || wireName == "-" {
					continue
				}
				goName := strings.ToLower(field.Name[:1]) + field.Name[1:]
				if wireName != goName {
					t.Errorf("%s JSON name = %q, want %q", field.Name, wireName, goName)
				}
			}
		})
	}
}
