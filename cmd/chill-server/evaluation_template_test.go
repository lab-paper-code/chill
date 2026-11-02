package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
)

func TestEvaluationTemplateSourcesAndLiveDigest(t *testing.T) {
	service, request := evaluationFixture(t)
	request.Spec.Target.Components[0].Image = "repo/server@sha256:stale"
	summary := experimentFixture("latest", time.Now().UTC())
	definition, err := json.Marshal(struct {
		Spec any               `json:"spec"`
		Run  map[string]string `json:"run"`
	}{request.Spec, map[string]string{"uid": "latest", "name": "published-run"}})
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
	if result.Sources["target"] != "live" || result.Sources["harness"] != "latestPublication" {
		t.Fatalf("sources=%+v", result.Sources)
	}
	if result.SourcePublication == nil ||
		result.SourcePublication.UID != "latest" || result.SourcePublication.Name != "published-run" {
		t.Fatalf("sourcePublication=%+v", result.SourcePublication)
	}
	for field, source := range result.Sources {
		switch source {
		case templateSourceLive, "config", templateSourcePublication, templateSourceNone:
		default:
			t.Fatalf("sources[%q]=%q", field, source)
		}
	}
}

func TestEvaluationTemplateWithoutPublication(t *testing.T) {
	service, _ := evaluationFixture(t)
	if err := appsv1.AddToScheme(service.client.Scheme()); err != nil {
		t.Fatal(err)
	}
	objects := &experimentMemoryStore{}
	service.experiments = &experimentService{reader: objects, lister: objects, store: "evidence", maxBytes: 1 << 20}
	result, err := service.template(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SourcePublication != nil {
		t.Fatalf("sourcePublication=%+v", result.SourcePublication)
	}
	for _, field := range []string{"harness", "loadGenerator", "judgment", "control"} {
		if result.Sources[field] != templateSourceNone {
			t.Fatalf("sources[%q]=%q", field, result.Sources[field])
		}
	}
}
