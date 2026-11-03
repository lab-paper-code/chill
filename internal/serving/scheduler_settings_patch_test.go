package serving

import (
	"reflect"
	"testing"
)

func TestValidateSchedulerSettingsPatch(t *testing.T) {
	current, _ := schedulerFixture()
	for _, tc := range []struct {
		name, patch string
		invalid     bool
	}{
		{"one switch", `{"controls":{"power":false}}`, false},
		{"public numeric setting", `{"maxReplicas":3}`, false},
		{"public map", `{"minimumDemand":{"work-uid":2}}`, false},
		{"unknown", `{"mystery":true}`, true},
		{"unknown null", `{"mystery":null}`, true},
		{"unknown nested", `{"controls":{"mystery":true}}`, true},
		{"bad type", `{"controls":{"power":"false"}}`, true},
		{"invalid resulting value", `{"maxReplicas":0}`, true},
		{"array", `[]`, true},
		{"null", `null`, true},
		{"trailing JSON", `{} {}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := current
			got, err := ValidateSchedulerSettingsPatch(current, []byte(tc.patch))
			if (err != nil) != tc.invalid {
				t.Fatalf("patch=%s err=%v", tc.patch, err)
			}
			if !reflect.DeepEqual(current, before) {
				t.Fatal("validation mutated current settings")
			}
			if tc.name == "one switch" {
				expected := current
				expected.Controls.Power = false
				if !reflect.DeepEqual(got, expected) {
					t.Fatalf("undeclared settings changed: %+v", got)
				}
			}
		})
	}
}
