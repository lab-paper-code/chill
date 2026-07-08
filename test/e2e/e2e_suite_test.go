package e2e

import (
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/lab-paper-code/gearedge/test/utils"
)

var _ = BeforeSuite(func() {
	Expect(utils.RequireKindContext()).To(Succeed())
})

// Run e2e tests using the Ginkgo runner.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting gearedge suite\n")
	RunSpecs(t, "e2e suite")
}
