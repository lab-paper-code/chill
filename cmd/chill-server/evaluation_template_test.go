package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestEvaluationTemplateSourcesAndLiveDigest(t *testing.T) {
	service, request := evaluationFixture(t)
	request.Spec.Target.Components[0].Image = "repo/server@sha256:stale"
	summary := experimentFixture("latest", time.Now().UTC())
	definition, err := json.Marshal(struct {
		Spec any `json:"spec"`
	}{request.Spec})
	if err != nil {
		t.Fatal(err)
	}
	summary.Definition = definition
	objects := &experimentMemoryStore{}
	objects.add(t, summary)
	service.experiments = &experimentService{reader: objects, lister: objects, store: "evidence", maxBytes: 1 << 20}
	service.store = "configured-store"
	result, err := service.template(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Spec.Target.Components[0].Image != "repo/server@sha256:live" ||
		result.Spec.EvidenceStore != "configured-store" {
		t.Fatalf("spec=%+v", result.Spec)
	}
	if result.Sources["target"] != "live" || result.Sources["harness"] != "publication/latest" {
		t.Fatalf("sources=%+v", result.Sources)
	}
}
