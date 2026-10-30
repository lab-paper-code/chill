package operatorconsole

import "time"

// ModelCompatibilitySelection is one exact, Node-scoped static support query.
// Power-mode eligibility and measurement admission are deliberately separate.
type ModelCompatibilitySelection struct {
	ModelSpec     string `json:"modelSpec"`
	ExecutionPath string `json:"executionPath"`
	DeviceClass   string `json:"deviceClass"`
	Node          string `json:"node"`
}

type ModelCompatibilityObjectInput struct {
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
}

type ModelCompatibilityRuntimeInput struct {
	ModelCompatibilityObjectInput
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Image     string `json:"image"`
}

type ModelCompatibilityInputs struct {
	ModelSpec          ModelCompatibilityObjectInput   `json:"modelSpec"`
	DeviceClass        ModelCompatibilityObjectInput   `json:"deviceClass"`
	Node               ModelCompatibilityObjectInput   `json:"node"`
	RuntimeDeclaration *ModelCompatibilityRuntimeInput `json:"runtimeDeclaration"`
}

type ModelCompatibilityReason struct {
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ModelCompatibilitySnapshot struct {
	ReadAt    time.Time
	Selection ModelCompatibilitySelection
	Inputs    ModelCompatibilityInputs
	Verdict   string
	Reasons   []ModelCompatibilityReason
}

type ModelCompatibilityView struct {
	ReadAt    string                      `json:"readAt"`
	Selection ModelCompatibilitySelection `json:"selection"`
	Inputs    ModelCompatibilityInputs    `json:"inputs"`
	Verdict   string                      `json:"verdict"`
	Reasons   []ModelCompatibilityReason  `json:"reasons"`
}

type ModelCompatibilityMatrixView struct {
	ReadAt   string                   `json:"readAt"`
	Scope    string                   `json:"scope"`
	Rows     []ModelCompatibilityView `json:"rows"`
	NodeRows []ModelCompatibilityView `json:"nodeRows"`
}

func BuildModelCompatibility(snapshot ModelCompatibilitySnapshot) ModelCompatibilityView {
	reasons := snapshot.Reasons
	if reasons == nil {
		reasons = []ModelCompatibilityReason{}
	}
	return ModelCompatibilityView{
		ReadAt: snapshot.ReadAt.UTC().Format(time.RFC3339Nano), Selection: snapshot.Selection,
		Inputs: snapshot.Inputs, Verdict: snapshot.Verdict, Reasons: reasons,
	}
}
