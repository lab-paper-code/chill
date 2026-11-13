package v1alpha1_test

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func TestValidateWorkload(t *testing.T) {
	valid := edgev1alpha1.WorkloadSpec{
		ModelRef:        edgev1alpha1.CatalogReference{Name: "detector.v2"},
		PreprocessorRef: edgev1alpha1.CatalogReference{Name: "imagenet"},
		SLO:             edgev1alpha1.WorkloadSLO{Percentile: 99, TargetLatency: metav1.Duration{Duration: 100 * time.Millisecond}},
		Admission:       edgev1alpha1.DefaultWorkloadAdmission(),
	}
	if valid.Admission.Policy != edgev1alpha1.AdmissionPolicyReject || valid.Admission.MaxQueuedRequests != 1024 {
		t.Fatalf("admission defaults = %+v", valid.Admission)
	}
	for _, tc := range []struct {
		name      string
		admission edgev1alpha1.WorkloadAdmission
		field     string
	}{
		{"reject", edgev1alpha1.WorkloadAdmission{Policy: edgev1alpha1.AdmissionPolicyReject, MaxQueuedRequests: 1}, ""},
		{"delay", edgev1alpha1.WorkloadAdmission{Policy: edgev1alpha1.AdmissionPolicyDelay, MaxQueuedRequests: math.MaxInt32}, ""},
		{"empty policy", edgev1alpha1.WorkloadAdmission{MaxQueuedRequests: 1024}, "spec.admission.policy"},
		{"unknown policy", edgev1alpha1.WorkloadAdmission{Policy: "Keep", MaxQueuedRequests: 1024}, "spec.admission.policy"},
		{"zero queue", edgev1alpha1.WorkloadAdmission{Policy: edgev1alpha1.AdmissionPolicyReject}, "spec.admission.maxQueuedRequests"},
		{"negative queue", edgev1alpha1.WorkloadAdmission{Policy: edgev1alpha1.AdmissionPolicyReject, MaxQueuedRequests: -1}, "spec.admission.maxQueuedRequests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			spec.Admission = tc.admission
			err := edgev1alpha1.ValidateWorkload("detector-fast", spec)
			if tc.field == "" && err != nil || tc.field != "" && (err == nil || !strings.Contains(err.Error(), tc.field)) {
				t.Fatalf("admission %+v: %v", tc.admission, err)
			}
		})
	}
	for _, lifetime := range []time.Duration{0, -time.Second, 99 * time.Millisecond, 130 * time.Second} {
		spec := valid
		spec.RequestLifetime = &metav1.Duration{Duration: lifetime}
		if err := edgev1alpha1.ValidateWorkload("detector-fast", spec); err == nil || !strings.Contains(err.Error(), "spec.requestLifetime") {
			t.Fatalf("requestLifetime %s: %v", lifetime, err)
		}
	}
	for _, lifetime := range []time.Duration{100 * time.Millisecond, 129 * time.Second} {
		spec := valid
		spec.RequestLifetime = &metav1.Duration{Duration: lifetime}
		if err := edgev1alpha1.ValidateWorkload("detector-fast", spec); err != nil {
			t.Fatalf("requestLifetime %s: %v", lifetime, err)
		}
	}
	for name, spec := range map[string]edgev1alpha1.WorkloadSpec{
		"missing preprocessor": {ModelRef: valid.ModelRef, SLO: valid.SLO},
		"invalid dataset":      {ModelRef: valid.ModelRef, PreprocessorRef: valid.PreprocessorRef, DatasetRef: &edgev1alpha1.CatalogReference{Name: "VQA"}, SLO: valid.SLO},
	} {
		if err := edgev1alpha1.ValidateWorkload("detector-fast", spec); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	for _, tc := range []struct {
		name       string
		objectName string
		modelName  string
		percentile float64
		latency    time.Duration
		valid      bool
	}{
		{"valid", "detector-fast", "detector.v2", 99, 100 * time.Millisecond, true},
		{"upper percentile", "detector-fast", "detector", 99.9, time.Nanosecond, true},
		{"percentile without a sample floor", "detector-fast", "detector", 100, time.Second, false},
		{"fractional percentile", "detector-fast", "detector", 0.01, time.Second, true},
		{"empty object", "", "detector", 99, time.Second, false},
		{"invalid object", "Detector", "detector", 99, time.Second, false},
		{"long object", strings.Repeat("a", 254), "detector", 99, time.Second, false},
		{"empty model", "detector-fast", "", 99, time.Second, false},
		{"invalid model", "detector-fast", "detector/name", 99, time.Second, false},
		{"long model", "detector-fast", strings.Repeat("a", 254), 99, time.Second, false},
		{"zero percentile", "detector-fast", "detector", 0, time.Second, false},
		{"negative percentile", "detector-fast", "detector", -1, time.Second, false},
		{"excess percentile", "detector-fast", "detector", 101, time.Second, false},
		{"NaN", "detector-fast", "detector", math.NaN(), time.Second, false},
		{"positive infinity", "detector-fast", "detector", math.Inf(1), time.Second, false},
		{"negative infinity", "detector-fast", "detector", math.Inf(-1), time.Second, false},
		{"zero duration", "detector-fast", "detector", 99, 0, false},
		{"negative duration", "detector-fast", "detector", 99, -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			spec.ModelRef.Name = tc.modelName
			spec.SLO.Percentile = tc.percentile
			spec.SLO.TargetLatency.Duration = tc.latency
			if err := edgev1alpha1.ValidateWorkload(tc.objectName, spec); (err == nil) != tc.valid {
				t.Fatalf("ValidateWorkload() = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

func TestWorkloadDurationDecodingRejectsOverflowAndMissingUnits(t *testing.T) {
	for _, duration := range []string{"9223372036854775808ns", "100", "invalid"} {
		var spec edgev1alpha1.WorkloadSpec
		if err := json.Unmarshal([]byte(`{"modelRef":{"name":"detector"},"slo":{"percentile":99,"targetLatency":"`+duration+`"}}`), &spec); err == nil {
			t.Fatalf("accepted invalid duration %q", duration)
		}
	}
	var spec edgev1alpha1.WorkloadSpec
	if err := json.Unmarshal([]byte(`{"modelRef":{"name":"detector"},"slo":{"percentile":99,"targetLatency":"100ms"}}`), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.SLO.TargetLatency.Duration != 100*time.Millisecond {
		t.Fatalf("duration = %v", spec.SLO.TargetLatency.Duration)
	}
}

func TestWorkloadAdmissionSchemaDefaults(t *testing.T) {
	data, err := os.ReadFile("../../config/crd/bases/edge.dacs.io_workloads.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatal(err)
	}
	admission := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["admission"]
	var defaults edgev1alpha1.WorkloadAdmission
	if err := json.Unmarshal(admission.Default.Raw, &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults != edgev1alpha1.DefaultWorkloadAdmission() ||
		string(admission.Properties["policy"].Default.Raw) != `"Reject"` ||
		string(admission.Properties["maxQueuedRequests"].Default.Raw) != "1024" ||
		*admission.Properties["maxQueuedRequests"].Minimum != 1 {
		t.Fatalf("admission schema defaults or minimum changed: %+v", admission)
	}
}
