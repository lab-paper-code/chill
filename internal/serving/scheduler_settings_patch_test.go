package serving

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
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

func TestSettingsPatchMatchesPUTDecode(t *testing.T) {
	current, _ := schedulerFixture()
	before, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, patch string
		valid       bool
	}{
		{"required null", `{"completionMarginMS":null}`, false},
		{"omitted patch field keeps current value", `{}`, true},
		{"normal required value", `{"completionMarginMS":0}`, true},
		{"optional null uses PUT default", `{"bootstrapCapacityWaitNanos":null}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := jsonpatch.MergePatch(before, []byte(test.patch))
			if err != nil {
				t.Fatal(err)
			}
			var input SchedulerSettingsInput
			decoder := json.NewDecoder(bytes.NewReader(body))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				t.Fatal(err)
			}
			put, putErr := input.Settings()
			preflight, preflightErr := ValidateSchedulerSettingsPatch(current, []byte(test.patch))
			if (putErr == nil) != test.valid || (preflightErr == nil) != test.valid {
				t.Fatalf("PUT=%v preflight=%v wantValid=%v", putErr, preflightErr, test.valid)
			}
			if test.valid && !reflect.DeepEqual(preflight, put) {
				t.Fatalf("preflight=%+v PUT=%+v", preflight, put)
			}
		})
	}
}
