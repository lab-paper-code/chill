package application

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func TestSchedulerSettingsPUTAndPATCHControlOccupancyDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		scalar       int64
	}{
		{"PUT/scalar", "PUT", 5000000},
		{"PUT/zero", "PUT", 0},
		{"PATCH/scalar", "PATCH", 5000000},
		{"PATCH/zero", "PATCH", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile("../../../config/samples/serving-scheduler.json")
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			var input serving.SchedulerSettingsInput
			if err := json.Unmarshal(document["settings"], &input); err != nil {
				t.Fatal(err)
			}
			current, err := input.Settings() // same settings boundary as public GET
			if err != nil {
				t.Fatal(err)
			}
			scalar := tc.scalar
			if tc.method == "PATCH" {
				patch, marshalErr := json.Marshal(map[string]int64{"defaultDispatchOccupancyNanos": scalar})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				current, err = serving.ValidateSchedulerSettingsPatch(current, patch)
			} else {
				input.DefaultDispatchOccupancyNanos = scalar
				current, err = input.Settings() // public PUT uses this exact decode boundary
			}
			if err != nil {
				t.Fatal(err)
			}
			// Public PUT replaces only settings in the otherwise unchanged ConfigMap.
			if document["settings"], err = json.Marshal(current); err != nil {
				t.Fatal(err)
			}
			if raw, err = json.Marshal(document); err != nil {
				t.Fatal(err)
			}
			configuration, err := DecodeSchedulerConfiguration(string(raw), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			profile := &serving.Profile{ID: "profile", NodeName: "node", Batches: []serving.BatchProfile{{Batch: 1}, {Batch: 2}}}
			catalog := &ProfilesView{CatalogView: servingkube.CatalogView{Profiles: []serving.Profile{*profile}}}
			selected := *catalogOccupancy(configuration.Settings, catalog, map[string]*serving.Profile{profile.ID: profile})
			if len(selected) != 2 {
				t.Fatalf("catalog occupancy skipped batches: %+v", selected)
			}
			for _, o := range selected {
				if int64(o.PerRequest) != scalar || o.Provenance.Value != scalar || o.Provenance.Source != "default" {
					t.Fatalf("public settings not selected as the default: %+v want %d", o, scalar)
				}
			}
		})
	}
}
