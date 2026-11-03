package operatorintegration

import (
	"context"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func blackBoxLoad() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "edge.dacs.io/v1alpha1", "kind": "LoadScenario",
		"metadata": map[string]interface{}{"name": uniqueModelSpecName("blackbox-load"), "namespace": "default"},
		"spec": map[string]interface{}{
			"workloads":           []interface{}{map[string]interface{}{"name": "work", "uid": "work-uid", "generation": int64(1)}},
			"parts":               []interface{}{map[string]interface{}{"id": "steady", "durationSeconds": float64(10), "rates": map[string]interface{}{"work": float64(1)}}},
			"arrivalDistribution": map[string]interface{}{"work": "poisson"},
			"seeds":               map[string]interface{}{"work": "18446744073709551615"},
			"datasetRefs":         map[string]interface{}{"work": map[string]interface{}{"name": "dataset", "uid": "dataset-uid"}},
		},
	}}
}

var _ = Describe("Black-box evaluation API", func() {
	It("accepts inline uint64 seeds and Dataset references", func() {
		load := blackBoxLoad()
		Expect(k8sClient.Create(context.Background(), load)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), load)).To(Succeed()) })
	})
	It("rejects removed input fields and extra Dataset fields with strict CRD validation", func() {
		for _, field := range []string{"seedRef", "payloadManifestRef", "rateBasisRef", "kappaRef"} {
			load := blackBoxLoad()
			Expect(unstructured.SetNestedField(load.Object, map[string]interface{}{"store": "old", "key": "old", "size": int64(1)}, "spec", field)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), load, &client.CreateOptions{FieldValidation: metav1.FieldValidationStrict})).NotTo(Succeed(), field)
		}
		for _, field := range []string{"payload", "split", "fields"} {
			load := blackBoxLoad()
			Expect(unstructured.SetNestedField(load.Object, "forbidden", "spec", "datasetRefs", "work", field)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), load, &client.CreateOptions{FieldValidation: metav1.FieldValidationStrict})).NotTo(Succeed(), field)
		}
		load := blackBoxLoad()
		parts, _, err := unstructured.NestedSlice(load.Object, "spec", "parts")
		Expect(err).NotTo(HaveOccurred())
		parts[0].(map[string]interface{})["alpha"] = map[string]interface{}{"work": float64(1)}
		Expect(unstructured.SetNestedSlice(load.Object, parts, "spec", "parts")).To(Succeed())
		Expect(k8sClient.Create(context.Background(), load, &client.CreateOptions{FieldValidation: metav1.FieldValidationStrict})).NotTo(Succeed())
	})
	It("rejects seed overflow, bad strings and mismatched workload keys", func() {
		for _, seed := range []interface{}{"18446744073709551616", "-1", "+1", "1e2", "1.5", "", float64(1)} {
			load := blackBoxLoad()
			Expect(unstructured.SetNestedField(load.Object, seed, "spec", "seeds", "work")).To(Succeed())
			Expect(k8sClient.Create(context.Background(), load)).NotTo(Succeed())
		}
		for _, field := range []string{"seeds", "datasetRefs"} {
			load := blackBoxLoad()
			values, _, err := unstructured.NestedMap(load.Object, "spec", field)
			Expect(err).NotTo(HaveOccurred())
			values["other"] = values["work"]
			delete(values, "work")
			Expect(unstructured.SetNestedMap(load.Object, values, "spec", field)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), load)).NotTo(Succeed())
		}
	})
})

var _ = Describe("Opaque public settings patches", func() {
	It("creates a Run without mode or starting state and preserves the patch", func() {
		image := api.EvaluationImage{Image: "evaluation@sha256:identity", SourceSHA: "source"}
		run := &api.EvaluationRun{
			ObjectMeta: metav1.ObjectMeta{Name: uniqueModelSpecName("blackbox-run"), Namespace: "default"},
			Spec: api.EvaluationRunSpec{
				Question: "public black-box measurement", EvidenceStore: "evidence", Harness: image,
				Target:           api.TargetIdentity{Components: []api.EvaluationTargetComponent{{Name: "server", Resource: api.EvaluationResourceReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "default", Name: "server"}}}},
				LoadGenerator:    api.Generator{EvaluationImage: image, NodeName: "genesis1", Shards: map[string]int32{"work": 1}, Barrier: api.GeneratorBarrier{MinimumReadySamples: 1}},
				PowerMeasurement: api.PowerMeasurement{Meters: []api.EvaluationMeter{{ID: "meter", SourceIdentity: "source"}}, Nodes: []api.EvaluationObjectReference{{Name: "node", UID: "node-uid"}}, Series: "power", Sampling: api.PowerSampling{IntervalNanos: 1}},
				SettingsPatch:    &apiextensionsv1.JSON{Raw: []byte(`{"controls":{"power":false}}`)},
				Judgment:         api.Judgment{EnergyCoverage: api.EnergyCoverage{MaxSingleGapSeconds: 1}},
				Stages:           []api.StageTemplate{{ID: "stage", Row: "measurement", LoadScenarioRef: api.EvaluationObjectReference{Name: "load", UID: "load-uid"}}},
			},
		}
		Expect(k8sClient.Create(context.Background(), run)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(context.Background(), run)).To(Succeed()) })
		stored := &api.EvaluationRun{}
		Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(run), stored)).To(Succeed())
		Expect(stored.Spec.SettingsPatch.Raw).To(MatchJSON(`{"controls":{"power":false}}`))
		stored.Spec.SettingsPatch.Raw = []byte(`{"controls":{"power":true}}`)
		Expect(k8sClient.Update(context.Background(), stored)).NotTo(Succeed())
	})
})
