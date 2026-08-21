package operatorintegration

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var _ = Describe("profiling request samples", func() {
	DescribeTable("match the current API schema", func(name string, object client.Object) {
		path := filepath.Join("..", "..", "..", "config", "samples", name)
		payload, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(yaml.UnmarshalStrict(payload, object)).To(Succeed())

		ctx := context.Background()
		object.SetNamespace("default")
		Expect(k8sClient.Create(ctx, object)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, object) })
	},
		Entry(
			"BatchSaturation",
			"edge_v1alpha1_batchsaturation.yaml",
			&edgev1alpha1.BatchSaturation{},
		),
		Entry(
			"ModelLoadedIdleBaseline",
			"edge_v1alpha1_modelloadedidlebaseline.yaml",
			&edgev1alpha1.ModelLoadedIdleBaseline{},
		),
		Entry(
			"PowerModeTransitionCost",
			"edge_v1alpha1_powermodetransitioncost.yaml",
			&edgev1alpha1.PowerModeTransitionCost{},
		),
	)
})
