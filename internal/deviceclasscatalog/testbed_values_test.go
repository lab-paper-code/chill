package deviceclasscatalog

import (
	"os"
	"testing"

	"github.com/lab-paper-code/chill/internal/nodediscovery"
	"sigs.k8s.io/yaml"
)

func TestTestbedDeviceCatalogMatchesNodeDiscoverySignatures(t *testing.T) {
	raw, err := os.ReadFile("../../charts/chill/values-testbed.yaml")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var values struct {
		Discovery struct {
			Catalog Catalog `json:"catalog"`
		} `json:"discovery"`
		NodeDiscovery nodediscovery.SignatureCatalog `json:"nodeDiscovery"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(values.Discovery.Catalog.Classes) == 0 {
		t.Fatal("testbed discovery catalog has no classes")
	}
	if len(values.NodeDiscovery.Signatures) == 0 {
		t.Fatal("testbed node discovery has no signatures")
	}

	signatureLabels := make([]map[string]string, 0, len(values.NodeDiscovery.Signatures))
	for _, signature := range values.NodeDiscovery.Signatures {
		signatureLabels = append(signatureLabels, signature.Facts.Labels())
	}

	for _, class := range values.Discovery.Catalog.Classes {
		if !hasMatchingSignature(class.MatchLabels, signatureLabels) {
			t.Errorf("catalog class %q cannot be reached by any nodeDiscovery.signature facts", class.Name)
		}
	}
}

func hasMatchingSignature(selector map[string]string, signatureLabels []map[string]string) bool {
	for _, labels := range signatureLabels {
		if matchLabels(labels, selector) {
			return true
		}
	}
	return false
}
