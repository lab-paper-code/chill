package kube

import (
	"encoding/json"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	nvidiaLowMode       = "GPU_900"
	nvidiaReferenceMode = "GPU_1500"
)

func gpuMode(name string, mhz int32) edge.PowerMode {
	return edge.PowerMode{Name: name, Actuator: edge.PowerModeActuator{
		Kind:                 edge.PowerModeActuatorNvidiaLockedGPUClock,
		NvidiaLockedGPUClock: &edge.NvidiaLockedGPUClockTarget{MinMHz: mhz, MaxMHz: mhz},
	}}
}

func TestNvidiaObservationWithoutRequestKeepsFixedMode(t *testing.T) {
	class := &edge.DeviceClass{Spec: edge.DeviceClassSpec{PowerModes: []edge.PowerMode{
		{Name: "fixed", Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorFixed}},
		gpuMode(nvidiaLowMode, 900),
	}}}
	for _, mhz := range []int32{300, 900} {
		observed := gpuMode("observed", mhz).Actuator
		raw, err := json.Marshal(observed)
		if err != nil {
			t.Fatal(err)
		}
		node := &core.Node{ObjectMeta: meta.ObjectMeta{Labels: map[string]string{},
			Annotations: map[string]string{chillmeta.PowerActuator: string(raw)}}}
		if mode := readBackMode(node, class); mode != class.Spec.PowerModes[0].Name {
			t.Fatalf("unrequested %d MHz removed fixed support: %q", mhz, mode)
		}
		if node.Annotations[chillmeta.PowerActuator] != string(raw) {
			t.Fatal("mode classification overwrote physical MHz observation")
		}
		node.Labels[chillmeta.PowerMode] = nvidiaLowMode
		want := ""
		if mhz == 900 {
			want = nvidiaLowMode
		}
		if mode := readBackMode(node, class); mode != want {
			t.Fatalf("requested mode read-back=%q want=%q", mode, want)
		}
	}
}
