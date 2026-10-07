// Package triton binds verified frozen profiles to Triton's synchronous HTTP API.
package triton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	maxControlBytes     = 1 << 20
	maxInferenceBytes   = 64 << 20
	guardVersion        = "same-workload-uid-v1"
	ortVersion          = "1.27.0"
	pythonBackend       = "python"
	tritonRuntimeFamily = "triton"
	queueTimeoutCeiling = math.MaxInt64/int64(time.Microsecond) + 2
)

type Client struct{ HTTPClient *http.Client }

type Spec struct {
	Endpoint   string
	ModelName  string
	InstanceID string
	Node       serving.NodeReference
	Pool       string
	Profile    *serving.Profile
	Weight     int32
	Capacity   int
	Lanes      int
}

// number accepts protobuf JSON's quoted int64 fields as well as HTTP integers.
type number int64

func (n *number) UnmarshalJSON(raw []byte) error {
	value := string(raw)
	if len(value) > 0 && value[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		*n = number(parsed)
	}
	return err
}

type tensor struct {
	Name             string          `json:"name"`
	Datatype         string          `json:"datatype"`
	Shape            []number        `json:"shape"`
	DataType         string          `json:"data_type"`
	Dims             []number        `json:"dims"`
	Reshape          json.RawMessage `json:"reshape"`
	Optional         bool            `json:"optional"`
	AllowRaggedBatch bool            `json:"allow_ragged_batch"`
	IsShapeTensor    bool            `json:"is_shape_tensor"`
}

type configuration struct {
	Name          string   `json:"name"`
	Backend       string   `json:"backend"`
	Platform      string   `json:"platform"`
	MaxBatchSize  int      `json:"max_batch_size"`
	Input         []tensor `json:"input"`
	Output        []tensor `json:"output"`
	InstanceGroup []struct {
		Kind  string `json:"kind"`
		Count int    `json:"count"`
		GPUs  []int  `json:"gpus"`
	} `json:"instance_group"`
	Parameters map[string]struct {
		StringValue string `json:"string_value"`
	} `json:"parameters"`
	ModelTransactionPolicy struct {
		Decoupled bool `json:"decoupled"`
	} `json:"model_transaction_policy"`
	DynamicBatching *struct {
		MaxQueueDelay      number `json:"max_queue_delay_microseconds"`
		Preferred          []int  `json:"preferred_batch_size"`
		PriorityLevels     number `json:"priority_levels"`
		DefaultPriority    number `json:"default_priority_level"`
		DefaultQueuePolicy struct {
			MaxQueueSize         number `json:"max_queue_size"`
			AllowTimeoutOverride bool   `json:"allow_timeout_override"`
			DefaultTimeout       number `json:"default_timeout_microseconds"`
			TimeoutAction        string `json:"timeout_action"`
		} `json:"default_queue_policy"`
		PriorityQueuePolicy json.RawMessage `json:"priority_queue_policy"`
	} `json:"dynamic_batching"`
	SequenceBatching   json.RawMessage `json:"sequence_batching"`
	EnsembleScheduling json.RawMessage `json:"ensemble_scheduling"`
	BatchInput         json.RawMessage `json:"batch_input"`
	BatchOutput        json.RawMessage `json:"batch_output"`
}

type metadata struct {
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
	Platform string   `json:"platform"`
	Inputs   []tensor `json:"inputs"`
	Outputs  []tensor `json:"outputs"`
}

func safeIdentity(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func safeModelName(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return len(value) <= 512
}

func definitionIdentity(uid, value string) bool {
	if !safeIdentity(value) {
		return false
	}
	first, rest, ok := strings.Cut(value, "/")
	if !ok || first != uid {
		return false
	}
	generation, rest, ok := strings.Cut(rest, "/")
	if !ok {
		return false
	}
	modelUID, modelGeneration, ok := strings.Cut(rest, "/")
	if !ok || !safeIdentity(modelUID) {
		return false
	}
	workloadVersion, err := strconv.ParseInt(generation, 10, 64)
	if err != nil || workloadVersion < 1 {
		return false
	}
	modelVersion, err := strconv.ParseInt(modelGeneration, 10, 64)
	return err == nil && modelVersion > 0
}

func supported(datatype string) bool {
	switch datatype {
	case "BOOL", "UINT8", "UINT16", "UINT32", "UINT64", "INT8", "INT16", "INT32", "INT64", "FP16", "FP32", "FP64":
		return true
	}
	return false
}

func present(raw json.RawMessage) bool {
	return len(raw) != 0 && string(raw) != "null" && string(raw) != "{}" && string(raw) != "[]"
}

func validateConfig(spec Spec, config configuration, model metadata) error {
	profile := spec.Profile
	condition := profile.Condition
	if condition.RuntimeFamily != tritonRuntimeFamily || condition.Backend != pythonBackend || condition.NodeUID != spec.Node.UID || profile.NodeName != spec.Node.Name {
		return errors.New("profile is not native Triton/Python evidence for this node")
	}
	if config.Name != spec.ModelName || model.Name != spec.ModelName || config.Backend != pythonBackend || config.Platform != "" || model.Platform != pythonBackend || !slices.Equal(model.Versions, []string{"1"}) {
		return errors.New("native model/backend/version mismatch")
	}
	if config.ModelTransactionPolicy.Decoupled || present(config.SequenceBatching) || present(config.EnsembleScheduling) || present(config.BatchInput) || present(config.BatchOutput) {
		return errors.New("only synchronous stateless native execution is supported")
	}
	if len(config.InstanceGroup) != 1 || config.InstanceGroup[0].Kind != "KIND_CPU" || config.InstanceGroup[0].Count != 1 || len(config.InstanceGroup[0].GPUs) != 0 {
		return errors.New("native config must have exactly one CPU instance")
	}
	if err := validateQueue(spec, config); err != nil {
		return err
	}
	if err := validateRuntime(spec, config); err != nil {
		return err
	}
	return validateTensors(spec.Profile.Condition.Tensor.Tensors, config, model)
}

func validateQueue(spec Spec, config configuration) error {
	condition := spec.Profile.Condition
	dynamic := config.DynamicBatching
	if dynamic == nil || dynamic.MaxQueueDelay != 0 || !slices.Equal(dynamic.Preferred, []int{config.MaxBatchSize}) || int(dynamic.PriorityLevels) != spec.Lanes || int(dynamic.DefaultPriority) != spec.Lanes || int(dynamic.DefaultQueuePolicy.MaxQueueSize) != spec.Capacity || !dynamic.DefaultQueuePolicy.AllowTimeoutOverride || dynamic.DefaultQueuePolicy.DefaultTimeout != number(queueTimeoutCeiling) || (dynamic.DefaultQueuePolicy.TimeoutAction != "" && dynamic.DefaultQueuePolicy.TimeoutAction != "REJECT") || present(dynamic.PriorityQueuePolicy) {
		return errors.New("native queue/wait/priority configuration mismatch")
	}
	if condition.Triton == nil || config.MaxBatchSize != int(condition.Triton.MaxBatchSize) || spec.Lanes != int(condition.Triton.PriorityLevels) || spec.Capacity != int(condition.Triton.MaxQueueSize) {
		return errors.New("native settings differ from frozen profile")
	}
	return nil
}

func (c configuration) parameter(key string) string { return c.Parameters[key].StringValue }

func validateRuntime(spec Spec, config configuration) error {
	condition := spec.Profile.Condition
	parameter := config.parameter
	if parameter("chill_guard") != guardVersion || parameter("chill_runtime_family") != tritonRuntimeFamily || parameter("chill_provider") != "CPUExecutionProvider" || parameter("chill_ort_version") != ortVersion || parameter("chill_intra_op_threads") != strconv.Itoa(condition.CPU.ORTIntraOpThreads) || parameter("chill_inter_op_threads") != strconv.Itoa(condition.CPU.ORTInterOpThreads) || parameter("chill_artifact_path") != condition.Artifact.Path {
		return errors.New("loaded underlying runtime/CPU/artifact condition mismatch")
	}
	if err := validateActualCPU(spec, config); err != nil {
		return err
	}
	guardPath := parameter("TRITON_BATCH_STRATEGY_PATH")
	if !path.IsAbs(guardPath) || path.Clean(guardPath) != guardPath || path.Base(guardPath) != "batchstrategy.so" || path.Base(path.Dir(guardPath)) != spec.ModelName {
		return errors.New("mandatory native UID guard path missing")
	}
	// Triton rejects a missing/unloadable mandatory guard before model readiness.
	return nil
}

func validateActualCPU(spec Spec, config configuration) error {
	condition := spec.Profile.Condition
	parameter := config.parameter
	quota := strings.Fields(parameter("chill_cgroup_cpu_max"))
	if len(quota) != 2 {
		return errors.New("actual cgroup CPU quota missing")
	}
	cpuQuota, quotaErr := strconv.ParseInt(quota[0], 10, 64)
	cpuPeriod, periodErr := strconv.ParseInt(quota[1], 10, 64)
	limit, limitErr := resource.ParseQuantity(condition.CPU.Limit)
	milliLimit := limit.MilliValue()
	if quotaErr != nil || periodErr != nil || limitErr != nil || cpuQuota < 1 || cpuPeriod < 1 || milliLimit < 1 || cpuQuota > math.MaxInt64/1000 || milliLimit > math.MaxInt64/cpuPeriod || cpuQuota*1000 != milliLimit*cpuPeriod {
		return errors.New("actual cgroup CPU quota differs from profile")
	}
	var affinity []int
	var providers []string
	if json.Unmarshal([]byte(parameter("chill_affinity_cpus")), &affinity) != nil || len(affinity) < condition.CPU.ORTIntraOpThreads || parameter("chill_cpuset_effective") == "" || json.Unmarshal([]byte(parameter("chill_available_providers")), &providers) != nil || !slices.Contains(providers, "CPUExecutionProvider") {
		return errors.New("actual CPU affinity/provider evidence missing")
	}
	for index, cpu := range affinity {
		if cpu < 0 || index > 0 && cpu <= affinity[index-1] {
			return errors.New("actual CPU affinity evidence is invalid")
		}
	}
	return nil
}

func validateTensors(expected []modelcompat.InputTensor, config configuration, model metadata) error {
	var published []modelcompat.InputTensor
	if err := json.Unmarshal([]byte(config.parameter("chill_tensors")), &published); err != nil || !sameTensors(published, expected) {
		return errors.New("published tensor contract differs from frozen profile")
	}
	if len(config.Input) != len(expected) || len(model.Inputs) != len(expected) {
		return errors.New("actual input count differs from profile")
	}
	for _, input := range expected {
		if !supported(input.Datatype) {
			return fmt.Errorf("unsupported datatype %s (BYTES/BF16 unsupported)", input.Datatype)
		}
		var dimensions []number
		for _, dimension := range input.SampleShape {
			dimensions = append(dimensions, number(dimension))
		}
		if !matchesTensor(config.Input, input.Name, "TYPE_"+input.Datatype, dimensions, false) || !matchesTensor(model.Inputs, input.Name, input.Datatype, append([]number{-1}, dimensions...), true) {
			return errors.New("actual input metadata differs from frozen profile")
		}
	}
	if len(config.Output) == 0 || len(model.Outputs) != len(config.Output) {
		return errors.New("actual output metadata missing")
	}
	for _, output := range config.Output {
		datatype := strings.TrimPrefix(output.DataType, "TYPE_")
		if present(output.Reshape) {
			var reshape struct {
				Shape []number `json:"shape"`
			}
			decoder := json.NewDecoder(bytes.NewReader(output.Reshape))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&reshape); err != nil || len(reshape.Shape) != 0 || !slices.Equal(output.Dims, []number{1}) {
				return errors.New("unsupported native output reshape")
			}
		}
		if !supported(datatype) || output.Optional || output.AllowRaggedBatch || output.IsShapeTensor || !matchesTensor(model.Outputs, output.Name, datatype, append([]number{-1}, output.Dims...), true) {
			return errors.New("unsupported actual output contract")
		}
	}
	return nil
}

func sameTensors(left, right []modelcompat.InputTensor) bool {
	if len(left) != len(right) {
		return false
	}
	for _, expected := range right {
		count := 0
		for _, actual := range left {
			if actual.Name == expected.Name && actual.Datatype == expected.Datatype && slices.Equal(actual.SampleShape, expected.SampleShape) {
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	return true
}

func matchesTensor(tensors []tensor, name, datatype string, dims []number, meta bool) bool {
	count := 0
	for _, item := range tensors {
		if item.Name != name {
			continue
		}
		if meta {
			if item.Datatype != datatype || !slices.Equal(item.Shape, dims) {
				return false
			}
		} else if item.DataType != datatype || !slices.Equal(item.Dims, dims) || present(item.Reshape) || item.Optional || item.AllowRaggedBatch || item.IsShapeTensor {
			return false
		}
		count++
	}
	return count == 1
}

func (c *Client) transport() *http.Client {
	base := http.DefaultClient
	if c != nil && c.HTTPClient != nil {
		base = c.HTTPClient
	}
	client := *base
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func request(ctx context.Context, client *http.Client, method, endpoint string, body []byte, limit int64) (result serving.BackendResult, err error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return serving.BackendResult{}, err
	}
	// No replay: a transport failure cannot prove whether native execution ran.
	req.GetBody = nil
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return serving.BackendResult{}, &serving.Error{Code: "BackendUnavailable", Message: "native transport failed; execution completion unknown"}
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	result = serving.BackendResult{StatusCode: response.StatusCode, ContentType: response.Header.Get("Content-Type")}
	if err != nil {
		return result, &serving.Error{Code: "BackendUnavailable", Message: "native response transport failed; execution completion unknown"}
	}
	if int64(len(payload)) > limit {
		return result, &serving.Error{Code: "BackendResponseTooLarge", Message: "native response exceeds size bound; execution completion unknown"}
	}
	result.Body = payload
	result.Finished = true
	return result, nil
}

func validateSpec(spec Spec) error {
	endpoint, err := url.Parse(spec.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || !safeModelName(spec.ModelName) {
		return errors.New("native endpoint and safe model name required")
	}
	if spec.Profile == nil || !safeIdentity(spec.InstanceID) || !safeIdentity(spec.Node.UID) || spec.Node.Name == "" || spec.Pool == "" || spec.Profile.ID == "" || spec.Weight < 1 || spec.Capacity < 1 || spec.Lanes < 1 {
		return errors.New("complete native instance/profile specification required")
	}
	return nil
}

func (c *Client) Probe(ctx context.Context, spec Spec) (serving.ReadyInstance, error) {
	var empty serving.ReadyInstance
	if err := validateSpec(spec); err != nil {
		return empty, err
	}
	base := strings.TrimRight(spec.Endpoint, "/")
	modelBase := base + "/v2/models/" + url.PathEscape(spec.ModelName)
	client := c.transport()
	get := func(endpoint string, value any) error {
		result, err := request(ctx, client, http.MethodGet, endpoint, nil, maxControlBytes)
		if err != nil {
			return err
		}
		if result.StatusCode != http.StatusOK {
			return fmt.Errorf("native probe returned status %d", result.StatusCode)
		}
		if value != nil {
			return json.Unmarshal(result.Body, value)
		}
		return nil
	}
	for _, endpoint := range []string{base + "/v2/health/ready", modelBase + "/versions/1/ready"} {
		if err := get(endpoint, nil); err != nil {
			return empty, err
		}
	}
	var server struct {
		Name       string   `json:"name"`
		Version    string   `json:"version"`
		Extensions []string `json:"extensions"`
	}
	if err := get(base+"/v2", &server); err != nil {
		return empty, err
	}
	if server.Name != tritonRuntimeFamily || server.Version == "" || !slices.Contains(server.Extensions, "schedule_policy") {
		return empty, errors.New("official Triton schedule-policy metadata required")
	}
	var trace struct {
		Level     []string `json:"trace_level"`
		Rate      string   `json:"trace_rate"`
		Count     string   `json:"trace_count"`
		Frequency string   `json:"log_frequency"`
		Mode      string   `json:"trace_mode"`
	}
	if err := get(base+"/v2/trace/setting", &trace); err != nil {
		return empty, err
	}
	if !slices.Equal(trace.Level, []string{"TIMESTAMPS"}) || trace.Rate != "1" || trace.Count != "-1" || trace.Frequency != "1" || trace.Mode != tritonRuntimeFamily {
		return empty, errors.New("native per-request timestamp tracing is required")
	}
	var config configuration
	var model metadata
	if err := get(modelBase+"/versions/1/config", &config); err != nil {
		return empty, err
	}
	if err := get(modelBase+"/versions/1", &model); err != nil {
		return empty, err
	}
	if err := validateConfig(spec, config, model); err != nil {
		return empty, err
	}
	return serving.ReadyInstance{
		State:   serving.InstanceState{ID: spec.InstanceID, Node: spec.Node, Pool: spec.Pool, Profile: spec.Profile.ID, Ready: true, Waiting: map[string]int{}, Running: map[string]int{}, ObservedAt: time.Now().UTC()},
		Profile: spec.Profile, Weight: spec.Weight, Capacity: spec.Capacity, Lanes: spec.Lanes,
		Execute: func(ctx context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
			return execute(ctx, client, modelBase+"/versions/1/infer", spec, dispatch)
		},
	}, nil
}

func execute(ctx context.Context, client *http.Client, endpoint string, spec Spec, dispatch serving.Dispatch) (serving.BackendResult, error) {
	if dispatch.Definition == nil || dispatch.Input == nil {
		return serving.BackendResult{Finished: true}, errors.New("native dispatch definition and sample required")
	}
	definitionID := dispatch.Definition.Identity()
	nativeID := dispatch.RequestID + "~" + definitionID
	if !safeIdentity(nativeID) || strings.Contains(dispatch.RequestID, "~") || !safeIdentity(dispatch.Definition.UID) || !definitionIdentity(dispatch.Definition.UID, definitionID) || dispatch.Priority < 1 || dispatch.Priority > uint64(spec.Lanes) || dispatch.Elapsed < 0 || dispatch.Definition.SLO.TargetLatency <= 0 || !(dispatch.Definition.SLO.Percentile > 0 && dispatch.Definition.SLO.Percentile <= 100) {
		return serving.BackendResult{Finished: true}, errors.New("invalid authoritative native dispatch")
	}
	if dispatch.Assignment.Instance != spec.InstanceID || dispatch.Assignment.Pool != spec.Pool || dispatch.Assignment.Node != spec.Node || dispatch.Assignment.Profile != spec.Profile.ID || dispatch.Assignment.Artifact != spec.Profile.Condition.Artifact.Name || dispatch.Assignment.ExecutionPath != spec.Profile.Condition.Artifact.ExecutionPath {
		return serving.BackendResult{Finished: true}, errors.New("dispatch differs from probed native assignment")
	}
	body, err := encodeRequest(spec, dispatch, nativeID, definitionID)
	if err != nil {
		return serving.BackendResult{Finished: true}, err
	}
	if len(body) > maxInferenceBytes {
		return serving.BackendResult{Finished: true}, errors.New("native inference request exceeds size bound")
	}
	if !time.Now().Before(dispatch.Deadline) {
		return serving.BackendResult{Finished: true}, &serving.Error{Code: "RequestTimeout", Message: "request lifetime expired before native dispatch"}
	}
	result, err := request(ctx, client, http.MethodPost, endpoint, body, maxInferenceBytes)
	if err != nil {
		return result, err
	}
	return result, validateResponse(spec, nativeID, result)
}

func validateResponse(spec Spec, nativeID string, result serving.BackendResult) error {
	var response struct {
		ID           string `json:"id"`
		ModelName    string `json:"model_name"`
		ModelVersion string `json:"model_version"`
		Error        string `json:"error"`
		Parameters   struct {
			ActualBatch    number `json:"chill_actual_batch_size"`
			ActualRequests number `json:"chill_actual_batch_requests"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return &serving.Error{Code: "BackendResponseInvalid", Message: "native synchronous response is not valid JSON"}
	}
	if result.StatusCode == http.StatusOK {
		if response.ID != nativeID || response.ModelName != spec.ModelName || response.ModelVersion != "1" || response.Error != "" {
			return &serving.Error{Code: "BackendResponseInvalid", Message: "native synchronous response correlation mismatch"}
		}
		if response.Parameters.ActualBatch < 1 || response.Parameters.ActualBatch > number(spec.Profile.Condition.Triton.MaxBatchSize) || response.Parameters.ActualRequests < 1 || response.Parameters.ActualRequests > response.Parameters.ActualBatch {
			return &serving.Error{Code: "BackendResponseInvalid", Message: "native synchronous response lacks actual batch evidence"}
		}
	} else {
		return &serving.Error{Code: "BackendError", Message: "native executor rejected or failed the request"}
	}
	return nil
}

func encodeRequest(spec Spec, dispatch serving.Dispatch, nativeID, definitionID string) ([]byte, error) {
	contract := spec.Profile.Condition.Tensor.Tensors
	if len(dispatch.Input.Inputs) != len(contract) {
		return nil, errors.New("native request input count mismatch")
	}
	type input struct {
		Name     string          `json:"name"`
		Datatype string          `json:"datatype"`
		Shape    []int32         `json:"shape"`
		Data     json.RawMessage `json:"data"`
	}
	inputs := make([]input, 0, len(contract))
	for _, expected := range contract {
		var found *serving.TensorInput
		for index := range dispatch.Input.Inputs {
			actual := &dispatch.Input.Inputs[index]
			if actual.Name == expected.Name {
				if found != nil {
					return nil, errors.New("duplicate native request input")
				}
				found = actual
			}
		}
		if found == nil || found.Datatype != expected.Datatype || !slices.Equal(found.Shape, expected.SampleShape) || !json.Valid(found.Data) {
			return nil, errors.New("native request is not one declared sample")
		}
		shape := make([]int32, 1, len(found.Shape)+1)
		shape[0] = 1
		shape = append(shape, found.Shape...)
		inputs = append(inputs, input{Name: found.Name, Datatype: found.Datatype, Shape: shape, Data: found.Data})
	}
	remaining := time.Until(dispatch.Deadline)
	if remaining <= 0 {
		return nil, &serving.Error{Code: "RequestTimeout", Message: "request lifetime expired before native dispatch"}
	}
	// Native schedule-policy timeout is a queue deadline, never an SLO wait.
	timeoutMicros := int64((remaining-1)/time.Microsecond + 1)
	return json.Marshal(struct {
		ID         string         `json:"id"`
		Parameters map[string]any `json:"parameters"`
		Inputs     []input        `json:"inputs"`
	}{ID: nativeID, Parameters: map[string]any{
		"chill_workload_uid": dispatch.Definition.UID, "chill_definition": definitionID,
		"chill_slo_percentile": dispatch.Definition.SLO.Percentile, "chill_slo_target_ns": int64(dispatch.Definition.SLO.TargetLatency),
		"chill_elapsed_ns": int64(dispatch.Elapsed), "priority": dispatch.Priority, "timeout": timeoutMicros,
	}, Inputs: inputs})
}
