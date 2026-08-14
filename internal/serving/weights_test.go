package serving

import "testing"

func backend(node string, weight int32) Backend {
	return Backend{Node: node, Service: "chill-serving-" + node, Weight: weight}
}

// TestChooseHonoursEveryShare walks one full cycle of draws so each node is
// chosen exactly as often as its weight, which is what "w is the share of
// traffic node n receives" means for integer weights.
func TestChooseHonoursEveryShare(t *testing.T) {
	weights := Weights{backend("jetsono", 1), backend("orangepi5", 3), backend("genesis2", 0)}

	chosen := make(map[string]int32)
	for pick := uint64(0); pick < 4; pick++ {
		got, err := weights.Choose(pick)
		if err != nil {
			t.Fatalf("Choose(%d) returned %v", pick, err)
		}
		chosen[got.Node]++
	}

	for _, want := range weights {
		if chosen[want.Node] != want.Weight {
			t.Errorf("node %q was chosen %d times across the cycle, want %d",
				want.Node, chosen[want.Node], want.Weight)
		}
	}
}

func TestChooseReducesTheDrawModuloTheTotal(t *testing.T) {
	weights := Weights{backend("jetsono", 1), backend("orangepi5", 3)}

	for pick := uint64(0); pick < 16; pick++ {
		got, err := weights.Choose(pick)
		if err != nil {
			t.Fatalf("Choose(%d) returned %v", pick, err)
		}
		want, err := weights.Choose(pick % 4)
		if err != nil {
			t.Fatalf("Choose(%d) returned %v", pick%4, err)
		}
		if got != want {
			t.Fatalf("Choose(%d) = %q, want the same node as Choose(%d) = %q",
				pick, got.Node, pick%4, want.Node)
		}
	}
}

func TestChooseReturnsTheServiceToCall(t *testing.T) {
	weights := Weights{backend("jetsono", 1)}

	got, err := weights.Choose(0)
	if err != nil {
		t.Fatalf("Choose(0) returned %v", err)
	}
	if got.Service != "chill-serving-jetsono" {
		t.Fatalf("Choose(0).Service = %q, want the chosen node's Service", got.Service)
	}
}

func TestWeightsValidateRejects(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		weights Weights
	}{
		{name: "a model no node serves", weights: Weights{}},
		{name: "a vector no node carries weight in", weights: Weights{backend("jetsono", 0)}},
		{name: "a negative share", weights: Weights{backend("jetsono", -1), backend("orangepi5", 2)}},
		{name: "one node entered twice", weights: Weights{backend("jetsono", 1), backend("jetsono", 2)}},
		{name: "a backend naming no node", weights: Weights{{Service: "chill-serving-jetsono", Weight: 1}}},
		{name: "a backend naming no Service", weights: Weights{{Node: "jetsono", Weight: 1}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.weights.Validate(); err == nil {
				t.Fatal("Validate accepted the vector, want an error")
			}
			if _, err := testCase.weights.Choose(0); err == nil {
				t.Fatal("Choose accepted the vector, want an error")
			}
		})
	}
}
