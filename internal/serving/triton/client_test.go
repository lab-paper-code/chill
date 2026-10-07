package triton

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
)

func nativeFixture(t *testing.T) (Spec, configuration, metadata) {
	t.Helper()
	inputs := []modelcompat.InputTensor{{Name: "features", Datatype: "FP32", SampleShape: []int32{2}}, {Name: "mask", Datatype: "BOOL", SampleShape: []int32{2}}}
	spec := Spec{ModelName: "model", InstanceID: "instance", Node: serving.NodeReference{Name: "node", UID: "node-uid"}, Pool: "pool", Weight: 1, Capacity: 7, Lanes: 3,
		Profile: &serving.Profile{ID: "profile", NodeName: "node", Condition: profilederivation.CurvePlanSpec{
			NodeUID: "node-uid", RuntimeFamily: tritonRuntimeFamily, Backend: "python",
			Triton:            &modelcompat.TritonSettings{MaxBatchSize: 2, PriorityLevels: 3, MaxQueueSize: 7},
			CPU:               profilederivation.CurveCPUContract{Limit: "1", ORTIntraOpThreads: 1, ORTInterOpThreads: 1},
			Artifact:          profilederivation.CurveArtifactContract{Name: "artifact", Path: "/artifact/model.onnx", ExecutionPath: "cpu"},
			PlannedBatchSizes: []int{1, 2}, Tensor: profilederivation.CurveTensorContract{Tensors: inputs},
		}, Batches: []serving.BatchProfile{{Batch: 1, MemoryBytes: 100, DurationNanos: []float64{10}}, {Batch: 2, MemoryBytes: 110, DurationNanos: []float64{12}}}},
	}
	var config configuration
	if err := json.Unmarshal([]byte(`{"name":"model","backend":"python","max_batch_size":2,"input":[{"name":"features","data_type":"TYPE_FP32","dims":["2"]},{"name":"mask","data_type":"TYPE_BOOL","dims":["2"]}],"output":[{"name":"result","data_type":"TYPE_FP32","dims":["2"]}],"instance_group":[{"kind":"KIND_CPU","count":1}],"dynamic_batching":{"max_queue_delay_microseconds":"0","priority_levels":"3","default_priority_level":"3","default_queue_policy":{"max_queue_size":"7"}},"parameters":{}}`), &config); err != nil {
		t.Fatal(err)
	}
	config.DynamicBatching.Preferred = []int{2}
	config.DynamicBatching.DefaultQueuePolicy.AllowTimeoutOverride = true
	config.DynamicBatching.DefaultQueuePolicy.DefaultTimeout = number(queueTimeoutCeiling)
	config.DynamicBatching.DefaultQueuePolicy.TimeoutAction = "REJECT"
	values := map[string]string{
		"chill_guard": guardVersion, "chill_runtime_family": tritonRuntimeFamily, "chill_provider": "CPUExecutionProvider", "chill_ort_version": ortVersion,
		"chill_intra_op_threads": "1", "chill_inter_op_threads": "1", "chill_artifact_path": "/artifact/model.onnx",
		"TRITON_BATCH_STRATEGY_PATH": "/models/model/batchstrategy.so", "chill_cgroup_cpu_max": "100000 100000",
		"chill_affinity_cpus": "[0]", "chill_cpuset_effective": "0", "chill_available_providers": `["CPUExecutionProvider"]`,
	}
	published, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	values["chill_tensors"] = string(published)
	for key, value := range values {
		config.Parameters[key] = struct {
			StringValue string `json:"string_value"`
		}{value}
	}
	var model metadata
	if err := json.Unmarshal([]byte(`{"name":"model","platform":"python","versions":["1"],"inputs":[{"name":"features","datatype":"FP32","shape":[-1,2]},{"name":"mask","datatype":"BOOL","shape":[-1,2]}],"outputs":[{"name":"result","datatype":"FP32","shape":[-1,2]}]}`), &model); err != nil {
		t.Fatal(err)
	}
	return spec, config, model
}

func TestFrozenNativeCondition(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Spec, *configuration, *metadata)
	}{
		{"legacy engine evidence", func(s *Spec, _ *configuration, _ *metadata) { s.Profile.Condition.RuntimeFamily = "onnxruntime" }},
		{"native maximum differs from frozen settings", func(_ *Spec, c *configuration, _ *metadata) {
			c.MaxBatchSize = 3
			c.DynamicBatching.Preferred = []int{3}
		}},
		{"native extra wait", func(_ *Spec, c *configuration, _ *metadata) { c.DynamicBatching.MaxQueueDelay = 1 }},
		{"ineffective zero native timeout", func(_ *Spec, c *configuration, _ *metadata) { c.DynamicBatching.DefaultQueuePolicy.DefaultTimeout = 0 }},
		{"replicated native instance", func(_ *Spec, c *configuration, _ *metadata) { c.InstanceGroup[0].Count = 2 }},
		{"missing guard", func(_ *Spec, c *configuration, _ *metadata) { delete(c.Parameters, "TRITON_BATCH_STRATEGY_PATH") }},
		{"wrong CPU quota", func(s *Spec, _ *configuration, _ *metadata) { s.Profile.Condition.CPU.Limit = "2" }},
		{"multi input shape drift", func(_ *Spec, _ *configuration, m *metadata) { m.Inputs[1].Shape[1] = 3 }},
		{"wrong underlying runtime", func(_ *Spec, c *configuration, _ *metadata) {
			delete(c.Parameters, "chill_ort_version")
		}},
		{"unsupported output", func(_ *Spec, c *configuration, _ *metadata) { c.Output[0].Optional = true }},
		{"metadata output drift", func(_ *Spec, _ *configuration, m *metadata) { m.Outputs[0].Shape[1] = 3 }},
	}
	spec, config, model := nativeFixture(t)
	if err := validateConfig(spec, config, model); err != nil {
		t.Fatalf("valid native condition: %v", err)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			spec, config, model := nativeFixture(t)
			test.change(&spec, &config, &model)
			if err := validateConfig(spec, config, model); err == nil {
				t.Fatal("unsafe native condition accepted")
			}
		})
	}
}

func TestServingRejectsBatchOverrideAndPriority(t *testing.T) {
	for _, bug := range []string{"sample batch axis", "duplicate input", "zero priority", "unselected priority", "missing definition", "expired lifetime"} {
		t.Run(bug, func(t *testing.T) {
			spec, _, _ := nativeFixture(t)
			dispatch := serving.Dispatch{RequestID: "request", Definition: &serving.Definition{UID: "workload", Generation: 1, Model: serving.Model{UID: "model-uid", Generation: 1}, SLO: serving.SLO{Percentile: 99, TargetLatency: time.Second}}, Priority: 1,
				Assignment: serving.Assignment{Instance: spec.InstanceID, Pool: spec.Pool, Node: spec.Node, Profile: spec.Profile.ID, Artifact: "artifact", ExecutionPath: "cpu"},
				Deadline:   time.Now().Add(time.Second),
				Input:      &serving.InferenceInput{Inputs: []serving.TensorInput{{Name: "features", Datatype: "FP32", Shape: []int32{2}, Data: json.RawMessage(`[1,2]`)}, {Name: "mask", Datatype: "BOOL", Shape: []int32{2}, Data: json.RawMessage(`[true,false]`)}}},
			}
			switch bug {
			case "sample batch axis":
				dispatch.Input.Inputs[0].Shape = []int32{1, 2}
			case "duplicate input":
				dispatch.Input.Inputs[1].Name = "features"
			case "zero priority":
				dispatch.Priority = 0
			case "unselected priority":
				dispatch.Priority = 4
			case "missing definition":
				dispatch.Definition = nil
			case "expired lifetime":
				dispatch.Deadline = time.Now().Add(-time.Second)
			}
			result, err := execute(context.Background(), nil, "unused", spec, dispatch)
			if bug == "expired lifetime" {
				failure, ok := err.(*serving.Error)
				if !ok || failure.Code != "RequestTimeout" || !result.Finished {
					t.Fatalf("unsent expired request was not terminal: %#v %v", result, err)
				}
			} else if err == nil || !result.Finished {
				t.Fatalf("unsent invalid request retained a native executor: %#v %v", result, err)
			}
		})
	}
}

func TestSynchronousCompletionProof(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if truncated {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				if _, err := fmt.Fprint(w, `{"error":"native queue unavailable"}`); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			result, err := request(context.Background(), server.Client(), http.MethodPost, server.URL, nil, maxInferenceBytes)
			if truncated {
				if err == nil || result.Finished {
					t.Fatal("incomplete transport proved execution finished")
				}
			} else if err != nil || !result.Finished || result.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("completed native synchronous error lost: %#v %v", result, err)
			}
		})
	}
}

func TestNativeResponseCorrelation(t *testing.T) {
	const nativeID = "request~workload/1/model-uid/1"
	spec, _, _ := nativeFixture(t)
	for _, test := range []struct {
		name, id, model, version string
		batch, requests          int
		valid                    bool
	}{
		{"correlated response", nativeID, spec.ModelName, "1", 2, 2, true},
		{"wrong request", "other", spec.ModelName, "1", 2, 2, false},
		{"wrong model", nativeID, "other", "1", 2, 2, false},
		{"wrong version", nativeID, spec.ModelName, "2", 2, 2, false},
		{"unmeasured actual batch", nativeID, spec.ModelName, "1", 3, 2, false},
		{"missing actual requests", nativeID, spec.ModelName, "1", 2, 0, false},
		{"impossible actual requests", nativeID, spec.ModelName, "1", 1, 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":%q,"model_name":%q,"model_version":%q,"parameters":{"chill_actual_batch_size":%d,"chill_actual_batch_requests":%d}}`,
				test.id, test.model, test.version, test.batch, test.requests)
			result := serving.BackendResult{StatusCode: http.StatusOK, Body: []byte(body), Finished: true}
			if err := validateResponse(spec, nativeID, result); (err == nil) != test.valid {
				t.Fatalf("native response correlation: %v", err)
			}
		})
	}
}
