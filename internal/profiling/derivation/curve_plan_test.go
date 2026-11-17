package profilederivation

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestCloneCurvePlanSpecOwnsActorRecipe(t *testing.T) {
	const changed = "changed"
	spec := CurvePlanSpec{Actor: firstfloor.Config{
		Inputs: []string{"input"}, Outputs: []string{"output"},
		RunnerCommand: []string{"runner"}, BatchSizes: []int{1, 2},
		WarmupBatchSizes: []int{1, 2},
		ArtifactFiles:    []string{"/artifact/model.onnx"},
		RuntimePackages:  map[string]string{"onnxruntime": "1.27"},
	}}
	got := CloneCurvePlanSpec(spec)
	spec.Actor.Inputs[0] = changed
	spec.Actor.Outputs[0] = changed
	spec.Actor.RunnerCommand[0] = changed
	spec.Actor.BatchSizes[0] = 99
	spec.Actor.WarmupBatchSizes[0] = 99
	spec.Actor.ArtifactFiles[0] = "/changed"
	spec.Actor.RuntimePackages["onnxruntime"] = changed
	if got.Actor.Inputs[0] != "input" || got.Actor.Outputs[0] != "output" ||
		got.Actor.RunnerCommand[0] != "runner" || got.Actor.BatchSizes[0] != 1 ||
		got.Actor.WarmupBatchSizes[0] != 1 || got.Actor.RuntimePackages["onnxruntime"] != "1.27" ||
		got.Actor.ArtifactFiles[0] != "/artifact/model.onnx" {
		t.Fatalf("caller mutated cloned actor recipe: %+v", got)
	}
}
