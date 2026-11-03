package service

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

func TestEvaluationTemplateSourcesAndExternalTarget(t *testing.T) {
	service, request := evaluationFixture(t)
	summary := PublicationDefinition{ID: "latest"}
	definition, err := json.Marshal(struct {
		Spec any               `json:"spec"`
		Run  map[string]string `json:"run"`
	}{request.Spec, map[string]string{"uid": "latest", "name": "published-run"}})
	if err != nil {
		t.Fatal(err)
	}
	summary.Definition = definition
	result, err := service.Template(context.Background(), "configured-store", []PublicationDefinition{summary})
	if err != nil {
		t.Fatal(err)
	}
	if result.Spec.Target.Components[0].Resource != request.Spec.Target.Components[0].Resource ||
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
	result, err := service.Template(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourcePublication != nil {
		t.Fatalf("sourcePublication=%+v", result.SourcePublication)
	}
	for _, field := range []string{"harness", "loadGenerator", "judgment", "settingsPatch"} {
		if result.Sources[field] != templateSourceNone {
			t.Fatalf("sources[%q]=%q", field, result.Sources[field])
		}
	}
}
