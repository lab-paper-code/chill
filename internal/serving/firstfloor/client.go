// Package firstfloor binds measured serving profiles to the native executor.
package firstfloor

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
	"slices"
	"strings"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

const maxControlBytes = 4 << 20
const phaseFinished = "finished"

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
	// ExpectedRuntime comes from verified profile evidence, not desired metadata.
	ExpectedRuntime *native.RuntimeMetadata
	DecisionID      string
}

func (c *Client) request(ctx context.Context, method, endpoint string, body []byte, limit int64) ([]byte, int, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, 0, errors.New("invalid native endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	err = errors.Join(readErr, response.Body.Close())
	if err != nil {
		return nil, response.StatusCode, err
	}
	if int64(len(raw)) > limit {
		return nil, response.StatusCode, errors.New("native response exceeds bound")
	}
	return raw, response.StatusCode, nil
}
func decode(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("native response contains trailing JSON")
	}
	return nil
}
func modelURL(spec Spec, suffix string) string {
	return strings.TrimRight(spec.Endpoint, "/") + "/v2/models/" + url.PathEscape(spec.ModelName) + "/" + suffix
}
func (c *Client) State(ctx context.Context, spec Spec) (native.State, error) {
	var state native.State
	raw, status, err := c.request(ctx, http.MethodGet, modelURL(spec, "state"), nil, maxControlBytes)
	if err != nil {
		return state, err
	}
	if status != http.StatusOK {
		return state, fmt.Errorf("native state HTTP %d: %s", status, raw)
	}
	if err = decode(raw, &state); err != nil {
		return state, err
	}
	if state.At.IsZero() {
		return state, errors.New("native state has no observation timestamp")
	}
	return state, nil
}
func validateMetadata(spec Spec, actual native.Metadata) error {
	if spec.Profile == nil || spec.InstanceID == "" || spec.Node.UID == "" || spec.Capacity < 1 || spec.Weight < 1 {
		return errors.New("complete measured physical instance specification required")
	}
	condition := spec.Profile.Condition
	if condition.RuntimeFamily != "firstfloor" || condition.NodeUID != spec.Node.UID || spec.Profile.NodeName != spec.Node.Name {
		return errors.New("profile does not match native physical execution")
	}
	if actual.Model != spec.ModelName || actual.Mode != "serving" || actual.ProfileID != spec.Profile.ID || actual.AppliedAt.IsZero() || actual.Runtime.Synthetic || actual.Runtime.RuntimeVersion == "" || actual.MaxQueueRequests != spec.Capacity || actual.ModelInstanceCount != 1 || condition.Firstfloor == nil || actual.MaxBatchSize > int(condition.Firstfloor.MaxBatchSize) {
		return errors.New("native prepared model/profile/policy mismatch")
	}
	if native.EffectiveDeadlinePolicy(actual.Policy.DeadlinePolicy) != native.EffectiveDeadlinePolicy(condition.Firstfloor.DeadlinePolicy) {
		return errors.New("native deadline policy differs from frozen profile")
	}
	if spec.DecisionID != "" && actual.DecisionID != spec.DecisionID {
		return errors.New("native policy acknowledgment pending")
	}
	if actual.Runtime.IntraOpThreads != condition.CPU.ORTIntraOpThreads || actual.Runtime.InterOpThreads != condition.CPU.ORTInterOpThreads || len(actual.Runtime.CPUAffinity) < condition.CPU.ORTIntraOpThreads {
		return errors.New("native thread/affinity condition differs from profile")
	}
	if err := validateProfileInputs(spec, actual); err != nil {
		return err
	}
	return validateMeasuredRuntime(spec, actual.Runtime)
}

func validateProfileInputs(spec Spec, actual native.Metadata) error {
	condition := spec.Profile.Condition
	batches := make([]int, len(spec.Profile.Batches))
	for i, b := range spec.Profile.Batches {
		batches[i] = b.Batch
	}
	slices.Sort(batches)
	if !slices.Equal(actual.Runtime.Inputs, condition.Preprocessor.Inputs) || !slices.Equal(actual.Runtime.Outputs, condition.Preprocessor.Outputs) || !slices.Equal(actual.Runtime.InitializedBatches, batches) || len(batches) == 0 || actual.MaxBatchSize != batches[len(batches)-1] || actual.Runtime.WarmupIterations != condition.ActiveProtocol.WarmupIterations {
		return errors.New("actual native raw fields/batches/warmup differ from measured condition")
	}
	return nil
}

func validateMeasuredRuntime(spec Spec, actual native.RuntimeMetadata) error {
	if spec.ExpectedRuntime == nil {
		return errors.New("verified runtime/provider/device/affinity evidence unavailable")
	}
	measured := spec.ExpectedRuntime
	if !slices.Equal(actual.ModelFiles, measured.ModelFiles) || actual.Runtime != measured.Runtime || actual.RuntimeVersion != measured.RuntimeVersion || actual.NumpyVersion != measured.NumpyVersion || actual.Device != measured.Device || actual.GPUUUID != measured.GPUUUID || !slices.Equal(actual.Providers, measured.Providers) || !slices.Equal(actual.CPUAffinity, measured.CPUAffinity) {
		return errors.New("actual runtime/provider/device/affinity differs from verified profile")
	}
	return nil
}
func (c *Client) Probe(ctx context.Context, spec Spec) (serving.ReadyInstance, error) {
	state, err := c.State(ctx, spec)
	if err != nil {
		return serving.ReadyInstance{}, err
	}
	if !state.Ready {
		return serving.ReadyInstance{}, errors.New("native runner is not ready")
	}
	if err = validateMetadata(spec, state.Metadata); err != nil {
		return serving.ReadyInstance{}, err
	}
	result := serving.ReadyInstance{State: serving.InstanceState{ID: spec.InstanceID, Node: spec.Node, Pool: spec.Pool, Profile: spec.Profile.ID, Ready: true, ObservedAt: time.Now().UTC(), Native: &state}, Profile: spec.Profile, Weight: spec.Weight, Capacity: spec.Capacity}
	result.Execute = func(ctx context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
		return c.Execute(ctx, spec, dispatch)
	}
	return result, nil
}
func progress(p native.Progress) serving.BackendProgress {
	return serving.BackendProgress{RequestID: p.RequestID, WorkloadUID: p.WorkloadUID, Definition: p.Definition, Phase: p.Phase, At: p.At, Outcome: p.Outcome, BatchID: p.BatchID, BatchSize: p.BatchSize, ProfileID: p.ProfileID, DecisionID: p.DecisionID, QueueMS: p.QueueMS, ServiceMS: p.ServiceMS, BackendMS: p.BackendMS, PreprocessMS: p.PreprocessMS, PostprocessMS: p.PostprocessMS, FirstTokenMS: p.FirstTokenMS, Units: p.Units, NativeElapsedMS: p.NativeElapsedMS, ExecutionComplete: p.ExecutionComplete}
}
func (c *Client) Execute(ctx context.Context, spec Spec, d serving.Dispatch) (serving.BackendResult, error) {
	if d.Definition == nil || d.Input == nil || d.RequestID == "" || spec.Profile == nil {
		return serving.BackendResult{Finished: true}, errors.New("native dispatch requires frozen request context and measured profile")
	}
	lifetime := 0.0
	if !d.Deadline.IsZero() {
		lifetime = float64(d.Deadline.Sub(d.ReceivedAt)) / float64(time.Millisecond)
	}
	request := native.Request{ID: d.RequestID, Context: native.RequestContext{RequestID: d.RequestID, WorkloadUID: d.Definition.UID, Definition: d.Definition.Identity(), ReceivedAt: d.ReceivedAt, SenderElapsedMS: float64(d.Elapsed) / float64(time.Millisecond), SLO: native.SLO{Percentile: d.Definition.SLO.Percentile, TargetMS: float64(d.Definition.SLO.TargetLatency) / float64(time.Millisecond)}, LifetimeMS: lifetime}}
	request.Inputs = make([]native.Field, len(d.Input.Inputs))
	for i, field := range d.Input.Inputs {
		request.Inputs[i] = native.Field{Name: field.Name, Data: [][]byte{field.Data}}
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return serving.BackendResult{Finished: true}, err
	}
	started := time.Now()
	body, status, err := c.request(ctx, http.MethodPost, modelURL(spec, "infer"), raw, native.MaxFrameBytes)
	result := serving.BackendResult{Body: body, ContentType: "application/json", StatusCode: status, ClientSpan: time.Since(started)}
	if err != nil {
		return result, err
	}
	if status != http.StatusOK {
		var failure native.ErrorResponse
		if err = decode(body, &failure); err != nil {
			return result, err
		}
		if failure.Progress == nil {
			result.Finished = failure.ExecutionComplete
			return result, errors.New(failure.Error)
		}
		p := failure.Progress
		var outcome error
		switch {
		case !failure.ExecutionComplete:
		case status == http.StatusRequestTimeout && validDeadlineDrop(p, &d, spec.Profile.ID):
			outcome = &serving.Error{Code: "SLODeadlineDrop", Message: failure.Error}
		case status == http.StatusUnprocessableEntity && p.Outcome == native.OutcomePreprocessFailed && executed(p, &d, spec.Profile.ID):
			outcome = &serving.Error{Code: "PreprocessFailed", Message: failure.Error}
		case status == http.StatusInternalServerError && p.Outcome == native.OutcomeBatchError && executed(p, &d, spec.Profile.ID):
			outcome = &serving.Error{Code: "BackendError", Message: failure.Error}
		}
		if outcome == nil {
			return result, errors.New("native failure lacks correlated execution proof")
		}
		result.Finished = true
		converted := progress(*p)
		result.Progress = &converted
		return result, outcome
	}
	var response native.Response
	if err = decode(body, &response); err != nil {
		return result, err
	}
	p := response.Progress
	if response.ID != d.RequestID || response.ModelName != spec.ModelName || !response.ExecutionComplete || len(response.Outputs) == 0 || !executed(&p, &d, spec.Profile.ID) {
		return result, errors.New("native response lacks correlated actual completion proof")
	}
	result.Finished = true
	converted := progress(p)
	result.Progress = &converted
	return result, nil
}
func validDeadlineDrop(p *native.Progress, d *serving.Dispatch, profileID string) bool {
	return p.Outcome == native.OutcomeDeadlineDrop && correlated(p, d, profileID) &&
		p.BatchID == 0 && p.BatchSize == 0 && p.ServiceMS == 0 && p.BackendMS == 0
}

// executed reports a finished record of this request inside an actual batch.
func executed(p *native.Progress, d *serving.Dispatch, profileID string) bool {
	return correlated(p, d, profileID) && p.BatchSize >= 1
}

func correlated(p *native.Progress, d *serving.Dispatch, profileID string) bool {
	return p.RequestID == d.RequestID && p.WorkloadUID == d.Definition.UID &&
		p.Definition == d.Definition.Identity() && p.ProfileID == profileID &&
		p.Phase == phaseFinished && !p.At.IsZero() && p.ExecutionComplete && validNativeSpans(*p)
}

var ErrNotProgress = errors.New("not a firstfloor progress record")

func ParseProgress(line []byte) (serving.BackendProgress, error) {
	marker := []byte("CHILL_PROGRESS ")
	index := bytes.Index(line, marker)
	if index < 0 {
		return serving.BackendProgress{}, ErrNotProgress
	}
	var p native.Progress
	if err := decode(line[index+len(marker):], &p); err != nil {
		return serving.BackendProgress{}, err
	}
	if p.RequestID == "" || p.WorkloadUID == "" || p.Definition == "" || p.At.IsZero() || (p.Phase != "queued" && p.Phase != "deferred" && p.Phase != "running" && p.Phase != phaseFinished) || (p.Phase == phaseFinished && !p.ExecutionComplete) {
		return serving.BackendProgress{}, errors.New("invalid actual firstfloor progress")
	}
	if !validNativeSpans(p) {
		return serving.BackendProgress{}, errors.New("native progress contains invalid actual monotonic spans")
	}
	return progress(p), nil
}
func validNativeSpans(p native.Progress) bool {
	for _, value := range []float64{p.QueueMS, p.ServiceMS, p.BackendMS, p.PreprocessMS, p.PostprocessMS, p.FirstTokenMS, p.NativeElapsedMS} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return p.ServiceMS >= p.BackendMS && p.NativeElapsedMS >= p.QueueMS+p.ServiceMS
}
