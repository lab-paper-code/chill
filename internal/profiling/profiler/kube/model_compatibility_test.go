package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func compatibilitySelection() ModelCompatibilitySelection {
	selection := validCandidateSelection()
	return ModelCompatibilitySelection{ModelSpec: selection.ModelSpec, ExecutionPath: selection.ExecutionPath,
		DeviceClass: selection.DeviceClass, Node: selection.Node}
}

func TestModelCompatibilityUsesTrustedExactDeclarations(t *testing.T) {
	for _, test := range []struct {
		name, verdict, reason string
		mutate                func([]client.Object)
	}{
		{name: "compatible without ready or mode eligibility", verdict: "Compatible", mutate: func(objects []client.Object) {
			objects[1].(*edgev1alpha1.DeviceClass).Spec.PowerModes = nil
		}},
		{name: "trusted runtime architecture conflict", verdict: "Incompatible", reason: "RuntimeArchitectureMismatch", mutate: func(objects []client.Object) {
			config := objects[2].(*corev1.ConfigMap)
			config.Data["execution-facts.yaml"] = strings.ReplaceAll(validExecutionFacts(), `"architecture":"amd64"`, `"architecture":"arm64"`)
		}},
		{name: "artifact architecture conflict", verdict: "Incompatible", reason: "ArtifactArchitectureMismatch", mutate: func(objects []client.Object) {
			objects[0].(*edgev1alpha1.ModelSpec).Spec.Artifacts[0].Source.Architecture = "arm64"
		}},
		{name: "another class is not an incompatibility", verdict: "Unknown", reason: "RuntimeDeclarationMissing", mutate: func(objects []client.Object) {
			objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = strings.ReplaceAll(validExecutionFacts(), "lattepanda-3-delta-8g", "another-class")
		}},
		{name: "another backend is not an incompatibility", verdict: "Unknown", reason: "RuntimeDeclarationMissing", mutate: func(objects []client.Object) {
			objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = strings.ReplaceAll(validExecutionFacts(), `"ort-cpu"`, `"rknnlite"`)
		}},
		{name: "another family is not an incompatibility", verdict: "Unknown", reason: "RuntimeDeclarationMissing", mutate: func(objects []client.Object) {
			objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = strings.ReplaceAll(validExecutionFacts(), `"firstfloor"`, `"other"`)
		}},
		{name: "missing key", verdict: "Unknown", reason: "RuntimeDeclarationMissing", mutate: func(objects []client.Object) {
			objects[2].(*corev1.ConfigMap).Data = nil
		}},
		{name: "duplicate declarations", verdict: "Unknown", reason: "RuntimeDeclarationAmbiguous", mutate: func(objects []client.Object) {
			facts := validExecutionFacts()
			objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = facts + facts[strings.Index(facts, "  - deviceClass:"):]
		}},
		{name: "overlapping backend declarations", verdict: "Unknown", reason: "RuntimeDeclarationAmbiguous", mutate: func(objects []client.Object) {
			facts := validExecutionFacts()
			objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = facts + strings.Replace(facts[strings.Index(facts, "  - deviceClass:"):], `"backends":["ort-cpu"]`, `"backends":["ort-cpu","rknnlite"]`, 1)
		}},
		{name: "strict invalid declaration", verdict: "Unknown", reason: "RuntimeDeclarationInvalid", mutate: func(objects []client.Object) {
			objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = validExecutionFacts() + "unexpected: true\n"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects := append(validCandidateObjects(), validNode())
			test.mutate(objects)
			got, err := newCandidateResolver(t, objects...).ResolveModelCompatibility(context.Background(), compatibilitySelection())
			if err != nil || string(got.Verdict) != test.verdict {
				t.Fatalf("compatibility = %+v, %v", got, err)
			}
			if got.ModelSpec.UID != "model-uid" || got.DeviceClass.UID != "device-uid" || got.Node.UID != "node-uid" {
				t.Fatalf("lost API identities: %+v", got)
			}
			if test.reason == "" {
				if len(got.Reasons) != 0 || got.RuntimeDeclaration == nil || got.RuntimeDeclaration.Image != testRuntimeImage {
					t.Fatalf("unexpected compatible facts: %+v", got)
				}
			} else if len(got.Reasons) != 1 || string(got.Reasons[0].Code) != test.reason || got.Reasons[0].Message == "" {
				t.Fatalf("reasons = %+v", got.Reasons)
			}
			if test.verdict == "Unknown" && got.RuntimeDeclaration != nil {
				t.Fatalf("invented declaration identity: %+v", got.RuntimeDeclaration)
			}
		})
	}
}

func TestModelCompatibilityKeepsGPUDeclarationsNodeBound(t *testing.T) {
	objects := validCandidateObjects()
	objects[0].(*edgev1alpha1.ModelSpec).Spec.ExecutionPaths[0].Runtime.Backend = "ort-cuda"
	facts := strings.Replace(validExecutionFacts(), `"backends":["ort-cpu"]`, `"backends":["ort-cuda"]`, 1)
	objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = facts +
		"    node: lattepanda\n    access:\n      gpuUUID: GPU-actual-device\n      resources: {nvidia.com/gpu: '1'}\n"
	otherNode := validNode()
	otherNode.Name, otherNode.UID = "another-node", "another-node-uid"
	objects = append(objects, validNode(), otherNode)
	resolver := newCandidateResolver(t, objects...)
	selection := compatibilitySelection()
	got, err := resolver.ResolveModelCompatibility(context.Background(), selection)
	if err != nil || got.Verdict != modelcompat.VerdictCompatible {
		t.Fatalf("declared Node: %+v, %v", got, err)
	}
	selection.Node = otherNode.Name
	got, err = resolver.ResolveModelCompatibility(context.Background(), selection)
	if err != nil || got.Verdict != modelcompat.VerdictUnknown || got.Reasons[0].Code != "RuntimeDeclarationMissing" {
		t.Fatalf("same class another Node borrowed GPU declaration: %+v, %v", got, err)
	}
}

func TestModelCompatibilityDistinguishesMissingObjectsAndBadSelection(t *testing.T) {
	objects := append(validCandidateObjects(), validNode())
	resolver := newCandidateResolver(t, objects...)
	for _, field := range []string{"model", "class", "node"} {
		selection := compatibilitySelection()
		switch field {
		case "model":
			selection.ModelSpec = testMissingName
		case "class":
			selection.DeviceClass = testMissingName
		case "node":
			selection.Node = testMissingName
		}
		if _, err := resolver.ResolveModelCompatibility(context.Background(), selection); !apierrors.IsNotFound(err) {
			t.Fatalf("%s missing object: %v", field, err)
		}
	}
	selection := compatibilitySelection()
	selection.ExecutionPath = testMissingName
	if _, err := resolver.ResolveModelCompatibility(context.Background(), selection); !apierrors.IsBadRequest(err) {
		t.Fatalf("undeclared execution path: %v", err)
	}
	node := objects[len(objects)-1].(*corev1.Node)
	node.Labels = nil
	if _, err := newCandidateResolver(t, objects...).ResolveModelCompatibility(context.Background(), compatibilitySelection()); !apierrors.IsBadRequest(err) {
		t.Fatalf("foreign class Node: %v", err)
	}
}

type compatibilityFailingReader struct {
	client.Reader
	err error
}

func (r compatibilityFailingReader) Get(ctx context.Context, key types.NamespacedName, object client.Object, options ...client.GetOption) error {
	if _, ok := object.(*corev1.ConfigMap); ok {
		return r.err
	}
	return r.Reader.Get(ctx, key, object, options...)
}

func TestModelCompatibilityPreservesFactSourceFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		missing bool
	}{
		{"missing declaration source", apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "facts"), true},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "facts", errors.New("denied")), false},
		{"transport", errors.New("API unavailable"), false},
		{"cancelled", context.Canceled, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := compatibilityFailingReader{Reader: candidateFakeReader(t, append(validCandidateObjects(), validNode())...), err: test.err}
			resolver, err := NewKubernetesCandidateResolver(reader, candidateResolverOptions())
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolver.ResolveModelCompatibility(context.Background(), compatibilitySelection())
			if test.missing {
				if err != nil || got.Verdict != modelcompat.VerdictUnknown || got.Reasons[0].Code != "RuntimeDeclarationMissing" {
					t.Fatalf("missing facts: %+v, %v", got, err)
				}
			} else if !errors.Is(err, test.err) {
				t.Fatalf("failure hidden as support verdict: %+v, %v", got, err)
			}
		})
	}
}

func TestModelCompatibilityRejectsMalformedStoredAuthorities(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]client.Object)
	}{
		{"model identity", func(objects []client.Object) { objects[0].SetUID("") }},
		{"class identity", func(objects []client.Object) { objects[1].SetUID("") }},
		{"Node identity", func(objects []client.Object) { objects[len(objects)-1].SetUID("") }},
		{"fact source identity", func(objects []client.Object) { objects[2].SetUID("") }},
		{"class architecture", func(objects []client.Object) { objects[1].(*edgev1alpha1.DeviceClass).Spec.Architecture = "" }},
		{"class selector", func(objects []client.Object) {
			objects[1].(*edgev1alpha1.DeviceClass).Spec.NodeSelector = map[string]string{"bad key": "value"}
		}},
		{"broken artifact reference", func(objects []client.Object) {
			objects[0].(*edgev1alpha1.ModelSpec).Spec.ExecutionPaths[0].Artifact = testMissingName
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects := append(validCandidateObjects(), validNode())
			test.mutate(objects)
			if _, err := newCandidateResolver(t, objects...).ResolveModelCompatibility(context.Background(), compatibilitySelection()); err == nil {
				t.Fatal("malformed stored authority became a support verdict")
			}
		})
	}
}
