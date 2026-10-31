package kube

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestTargetlessFixedActionsRequireObservedAuthorization(t *testing.T) {
	old := serving.Action{ID: "authorized", Kind: operationPower, Node: serving.NodeReference{Name: "node", UID: "node-uid"}, From: "max", To: "eco", Fixed: true, Status: nativeOperationRunning}
	if authorizedFixedContinuation(old, nil) {
		t.Fatal("new Fixed saving was authorized without a selected target")
	}
	observed := []serving.Action{old}
	if !authorizedFixedContinuation(old, observed) {
		t.Fatal("previously authorized running Fixed work was blocked")
	}
	for _, change := range []struct {
		name string
		edit func(*serving.Action)
	}{
		{"new ID", func(a *serving.Action) { a.ID = "new" }},
		{"different node UID", func(a *serving.Action) { a.Node.UID += "-recreated" }},
		{"different target", func(a *serving.Action) { a.To = "sleep" }},
		{"different source", func(a *serving.Action) { a.From = "normal" }},
		{"non-Fixed action", func(a *serving.Action) { a.Fixed = false }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := old
			change.edit(&candidate)
			if authorizedFixedContinuation(candidate, observed) {
				t.Fatalf("unadmitted action reused authorization: %+v", candidate)
			}
		})
	}
	observed[0].Fixed = false
	if authorizedFixedContinuation(old, observed) {
		t.Fatal("a different class of action authorized new Fixed work")
	}
}
