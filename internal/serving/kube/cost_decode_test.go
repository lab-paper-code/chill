package kube

import "testing"

func TestDecodeCostJSONIgnoresRetiredWorkloadUIDs(t *testing.T) {
	var wire powerPreparationWire
	raw := []byte(`{"schemaVersion":"v","prepared":{"workloadUIDs":["old"],"targetNode":{"name":"n"}}}`)
	if err := decodeCostJSON(raw, &wire); err != nil || wire.Prepared == nil || wire.Prepared.TargetNode.Name != "n" {
		t.Fatalf("stored preparation with workloadUIDs rejected: %+v %v", wire, err)
	}
	if err := decodeCostJSON(append(raw, " {}"...), &wire); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
