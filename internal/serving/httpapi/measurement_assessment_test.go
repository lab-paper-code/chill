package httpapi

import (
	"encoding/json"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMeasurementListAssessmentPreservesNativeWireShape(t *testing.T) {
	list := api.BatchEnergyOptimizationList{TypeMeta: meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimizationList"}, ListMeta: meta.ListMeta{ResourceVersion: "list-rv"}, Items: []api.BatchEnergyOptimization{{TypeMeta: meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimization"}, ObjectMeta: meta.ObjectMeta{Namespace: "ns", Name: "request", UID: "uid", ResourceVersion: "item-rv", Generation: 2}, Status: api.BatchEnergyOptimizationStatus{Phase: "Failed", ObservedGeneration: 2}}}}
	assessment := json.RawMessage(`{"status":"failed","displayStatus":"problem","label":"problem"}`)
	raw, err := json.Marshal(projectMeasurementList(list, []json.RawMessage{assessment}))
	if err != nil { t.Fatal(err) }
	var native api.BatchEnergyOptimizationList
	if err := json.Unmarshal(raw, &native); err != nil { t.Fatal(err) }
	if native.Kind != list.Kind || native.APIVersion != list.APIVersion || native.ResourceVersion != "list-rv" || len(native.Items) != 1 || native.Items[0].UID != "uid" || native.Items[0].Generation != 2 || native.Items[0].Status.Phase != "Failed" { t.Fatalf("native fields lost: %s", raw) }
	var augmented struct { Items []struct { Assessment json.RawMessage `json:"assessment"` } `json:"items"` }
	if err := json.Unmarshal(raw, &augmented); err != nil { t.Fatal(err) }
	if len(augmented.Items) != 1 || string(augmented.Items[0].Assessment) != string(assessment) { t.Fatalf("assessment lost: %s", raw) }
}
