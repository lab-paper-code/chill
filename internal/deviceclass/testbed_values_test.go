package deviceclass

import (
	"os"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/nodeprobe"
	"sigs.k8s.io/yaml"
)

func TestDefaultDeviceCatalogMatchesNodeDiscoverySignatures(t *testing.T) {
	raw, err := os.ReadFile("../../charts/chill/values.yaml")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var values struct {
		Discovery struct {
			Catalog Catalog `json:"catalog"`
		} `json:"discovery"`
		NodeDiscovery nodeprobe.SignatureCatalog `json:"nodeDiscovery"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(values.Discovery.Catalog.Classes) == 0 {
		t.Fatal("default discovery catalog has no classes")
	}
	if len(values.NodeDiscovery.Signatures) == 0 {
		t.Fatal("default node discovery has no signatures")
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

	serverModels := map[string]bool{
		"z370-aorus-gaming-7": false,
		"z370-aorus-gaming-3": false,
		"z390-gaming-x":       false,
	}
	serverClassFound := false
	for _, class := range values.Discovery.Catalog.Classes {
		if class.Name != "edge-server-rtx-2080-ti-64g" {
			continue
		}
		serverClassFound = true
		for _, signature := range values.NodeDiscovery.Signatures {
			if matchLabels(signature.Facts.Labels(), class.MatchLabels) {
				if _, ok := serverModels[signature.Facts.Model]; ok {
					serverModels[signature.Facts.Model] = true
				}
			}
		}
	}
	if !serverClassFound {
		t.Fatal("default discovery catalog has no edge-server-rtx-2080-ti-64g class")
	}
	for model, found := range serverModels {
		if !found {
			t.Errorf("server model %q cannot reach edge-server-rtx-2080-ti-64g", model)
		}
	}
}

func TestDefaultPowerActuationPoliciesMatchCatalog(t *testing.T) {
	raw, err := os.ReadFile("../../charts/chill/values.yaml")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var values struct {
		PowerActuation struct {
			Policies []nodeactuator.ActuationPolicySpec `json:"policies"`
		} `json:"powerActuation"`
		Discovery struct {
			Catalog Catalog `json:"catalog"`
		} `json:"discovery"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(values.PowerActuation.Policies) == 0 {
		t.Fatal("default powerActuation policies are empty")
	}

	for i := range values.PowerActuation.Policies {
		policy := &values.PowerActuation.Policies[i]
		t.Run(policy.Name, func(t *testing.T) {
			if err := policy.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}

			var deviceClass *CatalogEntry
			for i := range values.Discovery.Catalog.Classes {
				candidate := &values.Discovery.Catalog.Classes[i]
				if candidate.Name == policy.DeviceClass {
					deviceClass = candidate
					break
				}
			}
			if deviceClass == nil {
				t.Fatalf("default catalog has no DeviceClass %q", policy.DeviceClass)
			}

			mode, err := FindPowerMode(deviceClass.PowerModes, policy.PowerMode)
			if err != nil {
				t.Fatalf("default catalog DeviceClass %q: %v", deviceClass.Name, err)
			}
			if mode.Actuator.Kind != edgev1alpha1.PowerModeActuatorKind(policy.ActuatorKind) {
				t.Fatalf("catalog actuator kind = %q, policy actuator kind = %q", mode.Actuator.Kind, policy.ActuatorKind)
			}
		})
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
