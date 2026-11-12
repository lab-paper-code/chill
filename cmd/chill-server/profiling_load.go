package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	gateway "github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

const profilingIssueLagTolerance = 10 * time.Millisecond

// These are the immutable LoadControl fields; profiling never constructs a
// measured serving profile or chooses a native service/bootstrap bound.
type profilingLoadPlan struct {
	NativeEndpoint          string                   `json:"nativeEndpoint"`
	InferPath               string                   `json:"inferPath"`
	Model                   string                   `json:"model"`
	Fields                  []string                 `json:"fields"`
	Batch                   int                      `json:"batch"`
	PodUID                  string                   `json:"podUID"`
	SLOPercentile           float64                  `json:"sloPercentile"`
	SLOTargetLatencyNanos   int64                    `json:"sloTargetLatencyNanos"`
	WorkloadResourceVersion string                   `json:"workloadResourceVersion"`
	OperatingPoints         profilingOperatingPoints `json:"operatingPoints"`
	MaxQueueRequests        int                      `json:"maxQueueRequests"`
	MaxQueueBytes           int64                    `json:"maxQueueBytes"`
	MinServiceSamples       int                      `json:"minServiceSamples"`
	MaxLatencySamples       int                      `json:"maxLatencySamples"`
	MaxResultBytes          int64                    `json:"maxResultBytes"`
}

type profilingOperatingPoints struct {
	Utilizations                   []float64 `json:"utilizations"`
	SparseMeanGapMilliseconds      float64   `json:"sparseMeanGapMilliseconds"`
	PhaseDurationSeconds           float64   `json:"phaseDurationSeconds"`
	RequestLifetimeSeconds         float64   `json:"requestLifetimeSeconds"`
	DeliveryMarginMilliseconds     float64   `json:"deliveryMarginMilliseconds"`
	FramedDecodedRequestUpperBytes int64     `json:"framedDecodedRequestUpperBytes"`
	QueueBytesCap                  int64     `json:"queueBytesCap"`
}

type profilingRecord struct {
	ID     string
	Inputs []native.Field
}

type profilingLoadResult struct {
	Status       string               `json:"status"`
	Phases       []profilingrun.Phase `json:"phases"`
	SupplySizing json.RawMessage      `json:"supplySizing"`
}

type profilingIssueEvidence struct {
	ID                           string                 `json:"id"`
	Kind                         string                 `json:"kind"`
	Utilization                  float64                `json:"utilization,omitempty"`
	ArrivalRatePerSecond         float64                `json:"arrivalRatePerSecond"`
	AchievedArrivalRatePerSecond float64                `json:"achievedArrivalRatePerSecond"`
	IssueElapsedSeconds          float64                `json:"issueElapsedSecondsMonotonic"`
	IssueLagToleranceMS          float64                `json:"issueLagToleranceMs"`
	IssueLag                     *profilingrun.IssueLag `json:"issueLagMs"`
}

type profilingLoadFailure struct {
	Status        string                  `json:"status"`
	Message       string                  `json:"error"`
	FailureReason string                  `json:"failureReason,omitempty"`
	IssueEvidence *profilingIssueEvidence `json:"issueEvidence,omitempty"`
	cause         error
}

func (e *profilingLoadFailure) Error() string { return e.Message }
func (e *profilingLoadFailure) Unwrap() error { return e.cause }

func profilingLoadCommand(args []string) error {
	flags := flag.NewFlagSet("profiling-load", flag.ContinueOnError)
	control := flags.String("control-endpoint", "", "measurement LoadControl HTTP endpoint")
	dataset := flags.String("dataset-dir", "", "raw record dataset directory")
	deadlineText := flags.String("deadline", "", "absolute RFC3339Nano deadline")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *control == "" || *dataset == "" || *deadlineText == "" || flags.NArg() != 0 {
		return errors.New("profiling-load requires --control-endpoint, --dataset-dir and --deadline")
	}
	deadline, err := time.Parse(time.RFC3339Nano, *deadlineText)
	if err != nil {
		return fmt.Errorf("measurement absolute deadline: %w", err)
	}
	if !deadline.After(time.Now()) {
		return errors.New("measurement absolute deadline already expired")
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	return profilingLoad(ctx, *control, *dataset, os.Stderr)
}

func profilingLoad(ctx context.Context, endpoint, dataset string, report io.Writer) (err error) {
	control := &http.Client{}
	defer control.CloseIdleConnections()
	defer func() {
		if err == nil {
			return
		}
		failure := &profilingLoadFailure{Status: "Failed", Message: err.Error(), cause: err}
		var typed *profilingLoadFailure
		if errors.As(err, &typed) {
			failure.FailureReason, failure.IssueEvidence = typed.FailureReason, typed.IssueEvidence
		}
		// The root deadline also governs failure uploads. Never extend a Run
		// merely to report its timeout or wait on a blocked control endpoint.
		uploadErr := profilingControlCall(ctx, control, endpoint, http.MethodPost, "/measurement/load-result", failure, nil)
		if uploadErr != nil {
			err = errors.Join(err, fmt.Errorf("upload failed load result: %w", uploadErr))
		}
	}()
	var plan profilingLoadPlan
	if err = profilingControlCall(ctx, control, endpoint, http.MethodGet, "/measurement/load", nil, &plan); err != nil {
		return err
	}
	if err = plan.validate(); err != nil {
		return err
	}
	records, err := loadProfilingRecords(ctx, dataset, plan)
	if err != nil {
		return err
	}
	driver := newProfilingLoadDriver(plan, records, endpoint, report)
	defer driver.close()
	result, err := driver.run(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if int64(len(encoded)) > plan.MaxResultBytes {
		return errors.New("native profile result exceeds frozen runtime result budget")
	}
	return profilingControlCall(ctx, control, endpoint, http.MethodPost, "/measurement/load-result", json.RawMessage(encoded), nil)
}

func profilingControlCall(ctx context.Context, client *http.Client, endpoint, method, path string, payload, target any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "http" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid measurement control HTTP endpoint")
	}
	if method != http.MethodGet || path != "/measurement/load" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	// GET /measurement/load long-polls model/actor readiness. Its finite
	// absolute root deadline must permit legitimate warmup longer than 30s.
	var body []byte
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, native.MaxFrameBytes+1))
	err = errors.Join(readErr, response.Body.Close())
	if err != nil {
		return err
	}
	if len(raw) > native.MaxFrameBytes {
		return errors.New("measurement control response exceeds result budget")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("measurement control %s HTTP %d: %.1024s", path, response.StatusCode, raw)
	}
	if target == nil {
		var object map[string]json.RawMessage
		if err := profilingDecode(raw, &object); err != nil || object == nil {
			return errors.New("measurement control response must be an object")
		}
		return nil
	}
	return profilingDecode(raw, target)
}

func profilingDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("measurement response contains trailing JSON")
	}
	return nil
}

func (p profilingLoadPlan) validate() error {
	points := p.OperatingPoints
	if p.Batch < 1 || p.MaxQueueRequests < p.Batch || p.MinServiceSamples < 1 || p.MaxLatencySamples < p.MinServiceSamples || p.MaxLatencySamples > math.MaxInt/p.Batch || p.MaxResultBytes < 1 || p.MaxQueueBytes < 1 || p.Model == "" || p.PodUID == "" || p.WorkloadResourceVersion == "" || len(p.Fields) == 0 {
		return errors.New("measurement load plan has invalid identity, sample or byte bounds")
	}
	if !profilingPositive(points.PhaseDurationSeconds) || !profilingPositive(points.SparseMeanGapMilliseconds) || !profilingPositive(points.RequestLifetimeSeconds) || !profilingPositive(points.DeliveryMarginMilliseconds) || points.DeliveryMarginMilliseconds/1000 >= points.RequestLifetimeSeconds || points.FramedDecodedRequestUpperBytes < 1 || points.FramedDecodedRequestUpperBytes > native.MaxFrameBytes || points.QueueBytesCap < 1 || !profilingPositive(p.SLOPercentile) || p.SLOPercentile > 100 || p.SLOTargetLatencyNanos < 1 || len(points.Utilizations) == 0 {
		return errors.New("measurement load plan has invalid frozen operating points")
	}
	if points.PhaseDurationSeconds > float64(math.MaxInt64)/1e9 || points.RequestLifetimeSeconds+points.DeliveryMarginMilliseconds/1000 > float64(math.MaxInt64)/1e9 {
		return errors.New("measurement load duration exceeds monotonic timer range")
	}
	for _, utilization := range points.Utilizations {
		if !profilingPositive(utilization) || utilization >= 1 {
			return errors.New("measurement utilizations must be finite, positive and below one")
		}
	}
	seen := make(map[string]bool, len(p.Fields))
	for _, field := range p.Fields {
		if field == "" || field == "." || field == ".." || filepath.Base(field) != field || seen[field] {
			return errors.New("measurement fields must be unique dataset file names")
		}
		seen[field] = true
	}
	u, err := url.Parse(p.NativeEndpoint)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid profiling native HTTP endpoint")
	}
	infer, err := url.ParseRequestURI(p.InferPath)
	if err != nil || infer.IsAbs() || infer.Host != "" || infer.RawQuery != "" || infer.Fragment != "" || infer.Path != "/v2/models/"+p.Model+"/infer" {
		return errors.New("profiling native infer path differs from frozen model")
	}
	return nil
}

func profilingPositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
func profilingNonnegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func loadProfilingRecords(ctx context.Context, directory string, plan profilingLoadPlan) ([]profilingRecord, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var records []profilingRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record := profilingRecord{ID: entry.Name(), Inputs: make([]native.Field, len(plan.Fields))}
		var framed int64
		for index, field := range plan.Fields {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			remaining := plan.OperatingPoints.FramedDecodedRequestUpperBytes - framed - 4
			if remaining < 0 {
				return nil, errors.New("dataset record exceeds frozen framed decoded request upper bytes")
			}
			file, err := os.Open(filepath.Join(directory, entry.Name(), field))
			if err != nil {
				return nil, err
			}
			data, readErr := io.ReadAll(io.LimitReader(file, remaining+1))
			err = errors.Join(readErr, file.Close())
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > remaining {
				return nil, errors.New("dataset record exceeds frozen framed decoded request upper bytes")
			}
			framed += 4 + int64(len(data))
			record.Inputs[index] = native.Field{Name: field, Data: [][]byte{data}}
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, errors.New("profiling dataset contains no records")
	}
	return records, ctx.Err()
}

type profilingLoadDriver struct {
	plan      profilingLoadPlan
	records   []profilingRecord
	endpoint  string
	control   *http.Client
	native    *gateway.Client
	transport *http.Transport
	report    io.Writer
}

func newProfilingLoadDriver(plan profilingLoadPlan, records []profilingRecord, endpoint string, report io.Writer) *profilingLoadDriver {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// No transport connection limit: waiting behind a pool limit would silently
	// reshape Poisson input. The scheduler fails explicitly at the frozen cap.
	transport.MaxIdleConnsPerHost = max(plan.MaxQueueRequests, 64)
	transport.MaxIdleConns = transport.MaxIdleConnsPerHost
	return &profilingLoadDriver{plan: plan, records: records, endpoint: endpoint, report: report, transport: transport, control: &http.Client{}, native: &gateway.Client{HTTPClient: &http.Client{Transport: transport, Timeout: time.Duration((plan.OperatingPoints.RequestLifetimeSeconds + plan.OperatingPoints.DeliveryMarginMilliseconds/1000) * 1e9)}}}
}

func (d *profilingLoadDriver) close() {
	d.transport.CloseIdleConnections()
	d.control.CloseIdleConnections()
}

func (d *profilingLoadDriver) run(ctx context.Context) (profilingLoadResult, error) {
	result := profilingLoadResult{Status: "Succeeded"}
	sparse, err := d.measuredPhase(ctx, profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 1000 / d.plan.OperatingPoints.SparseMeanGapMilliseconds})
	if err != nil {
		return result, err
	}
	result.Phases = append(result.Phases, sparse)
	if err := profilingControlCall(ctx, d.control, d.endpoint, http.MethodPost, "/measurement/supply", struct {
		NativeSamples []profilingrun.NativeServiceSample `json:"nativeSamples"`
	}{sparse.NativeSamples}, &result.SupplySizing); err != nil {
		return result, err
	}
	var sizing struct {
		Workers int `json:"workers"`
	}
	if err := json.Unmarshal(result.SupplySizing, &sizing); err != nil {
		return result, err
	}
	if sizing.Workers < d.plan.Batch || sizing.Workers > 64 || sizing.Workers > d.plan.MaxQueueRequests || sizing.Workers%d.plan.Batch != 0 {
		return result, errors.New("native saturation sizing exceeds frozen request/worker cap or fixed batch")
	}
	saturation, err := d.measuredPhase(ctx, profilingrun.Phase{ID: "saturation", Kind: "saturation", Workers: sizing.Workers})
	if err != nil {
		return result, err
	}
	result.Phases = append(result.Phases, saturation)
	throughput := float64(saturation.CompletedInputs) / saturation.MeasurementElapsedSeconds
	if err := d.checkLadderBudget(result.Phases, throughput); err != nil {
		return result, err
	}
	for index, utilization := range d.plan.OperatingPoints.Utilizations {
		phase, err := d.measuredPhase(ctx, profilingrun.Phase{ID: fmt.Sprintf("utilization-%d", index), Kind: "utilization", Utilization: utilization, ArrivalRatePerSecond: utilization * throughput})
		if err != nil {
			return result, err
		}
		result.Phases = append(result.Phases, phase)
	}
	return result, nil
}

func (d *profilingLoadDriver) measuredPhase(ctx context.Context, spec profilingrun.Phase) (profilingrun.Phase, error) {
	boundary := func(action string) (string, error) {
		var response struct {
			At string `json:"at"`
		}
		err := profilingControlCall(ctx, d.control, d.endpoint, http.MethodPost, "/measurement/phase", map[string]string{"id": spec.ID, "action": action}, &response)
		if err != nil {
			return "", err
		}
		if _, err := time.Parse(time.RFC3339Nano, response.At); err != nil {
			return "", fmt.Errorf("measurement phase boundary: %w", err)
		}
		return response.At, nil
	}
	started, err := boundary("start")
	if err != nil {
		return spec, err
	}
	phase, err := d.runPhase(ctx, spec)
	if err != nil {
		return phase, err
	}
	ended, err := boundary("end")
	phase.MeasurementStartedAt, phase.MeasurementEndedAt = started, ended
	return phase, err
}

func (d *profilingLoadDriver) checkLadderBudget(phases []profilingrun.Phase, throughput float64) error {
	if !profilingPositive(throughput) {
		return errors.New("measured saturation input throughput is invalid")
	}
	var measured, bytesPerInput float64
	for _, phase := range phases {
		raw, err := json.Marshal(phase)
		if err != nil {
			return err
		}
		if phase.CompletedInputs < 1 {
			return errors.New("cannot project native profile without measured inputs")
		}
		measured += float64(len(raw))
		bytesPerInput = max(bytesPerInput, float64(len(raw))/float64(phase.CompletedInputs))
	}
	projected := measured + 4096
	for _, utilization := range d.plan.OperatingPoints.Utilizations {
		expected := throughput * utilization * d.plan.OperatingPoints.PhaseDurationSeconds
		// Include six Poisson standard deviations, the known-gap sample floor
		// (plus the unknown first gap), and all in-flight requests draining.
		count := math.Ceil(expected+6*math.Sqrt(expected)) + float64(d.plan.MinServiceSamples+1)*float64(d.plan.Batch) + float64(d.plan.MaxQueueRequests)
		projected += math.Ceil(count*bytesPerInput) + 4096
	}
	// Keep LoadControl's immutable result schema unchanged. The Job log owns
	// the measured admission report, including rejected full-run projections.
	if d.report != nil {
		if err := json.NewEncoder(d.report).Encode(struct {
			MeasuredBytesPerInput     float64 `json:"measuredBytesPerInput"`
			ProjectedResultBytes      float64 `json:"projectedResultBytes"`
			MaxResultBytes            int64   `json:"maxResultBytes"`
			SaturationInputsPerSecond float64 `json:"measuredSaturationInputsPerSecond"`
		}{bytesPerInput, projected, d.plan.MaxResultBytes, throughput}); err != nil {
			return err
		}
	}
	if !profilingPositive(projected) || projected > float64(d.plan.MaxResultBytes) {
		return fmt.Errorf("projected native profile %.0f bytes exceeds result budget %d before utilization ladder (%.1f measured bytes/input, mu=%.3f/s)", projected, d.plan.MaxResultBytes, bytesPerInput, throughput)
	}
	return nil
}

type profilingCompletion struct {
	row      profilingrun.PhaseRequest
	response native.Response
	sent     time.Time
	lag      float64
	err      error
}

func (d *profilingLoadDriver) send(ctx context.Context, spec profilingrun.Phase, index int, scheduled time.Time, alerts chan<- error) profilingCompletion {
	record := d.records[index%len(d.records)]
	id := fmt.Sprintf("%s-%d", spec.ID, index)
	points := d.plan.OperatingPoints
	request := native.Request{ID: id, Inputs: record.Inputs, Context: native.RequestContext{RequestID: id, WorkloadUID: d.plan.PodUID, Definition: d.plan.WorkloadResourceVersion, AdmissionPolicy: "Delay", ReceivedAt: scheduled.UTC(), SenderElapsedMS: max(0, float64(time.Since(scheduled))/1e6), CompletionMarginMS: points.DeliveryMarginMilliseconds, SLO: native.SLO{Percentile: d.plan.SLOPercentile, TargetMS: float64(d.plan.SLOTargetLatencyNanos) / 1e6}, LifetimeMS: points.RequestLifetimeSeconds * 1000}}
	var lock sync.Mutex
	var sent time.Time
	var lag float64
	var lagErr error
	// GetConn runs after the shared gateway encoder, immediately before the
	// pooled HTTP exchange. This send boundary includes encoding in issue lag
	// and the entire connect/write/read exchange in client latency.
	trace := &httptrace.ClientTrace{GetConn: func(string) {
		lock.Lock()
		defer lock.Unlock()
		if !sent.IsZero() {
			return
		}
		sent = time.Now()
		lag = max(0, float64(sent.Sub(scheduled))/1e6)
		if spec.Kind != "saturation" && sent.Sub(scheduled) > profilingIssueLagTolerance {
			lagErr = fmt.Errorf("Poisson issue lag %.3fms exceeds schedule tolerance 10ms", lag)
			select {
			case alerts <- lagErr:
			default:
			}
		}
	}}
	raw, status, err := d.native.InferNative(httptrace.WithClientTrace(ctx, trace), strings.TrimRight(d.plan.NativeEndpoint, "/")+d.plan.InferPath, request)
	completed := time.Now()
	lock.Lock()
	result := profilingCompletion{sent: sent, lag: lag, err: err}
	issueErr := lagErr
	lock.Unlock()
	if err != nil {
		return result
	}
	if status != http.StatusOK {
		var failure native.ErrorResponse
		_ = json.Unmarshal(raw, &failure)
		result.err = &profilingLoadFailure{Status: "Failed", Message: fmt.Sprintf("native inference HTTP %d: %.1024s", status, raw)}
		if failure.ExecutionComplete && failure.FailureReason == "MemoryAllocationFailed" {
			result.err.(*profilingLoadFailure).FailureReason = failure.FailureReason
		}
		return result
	}
	if err := profilingDecode(raw, &result.response); err != nil {
		result.err = err
		return result
	}
	if sent.IsZero() {
		result.err = errors.New("native request lacks actual HTTP issue timestamp")
		return result
	}
	result.row = profilingrun.PhaseRequest{RequestID: id, RecordID: record.ID, ArrivalAt: scheduled.UTC(), SentAt: sent.UTC(), CompletedAt: completed.UTC(), LatencyMilliseconds: float64(completed.Sub(sent)) / 1e6}
	result.err = issueErr
	return result
}

func (d *profilingLoadDriver) runPhase(ctx context.Context, phase profilingrun.Phase) (profilingrun.Phase, error) {
	if len(d.records) == 0 || (phase.Kind != "saturation" && !profilingPositive(phase.ArrivalRatePerSecond)) || (phase.Kind == "saturation" && (phase.Workers < d.plan.Batch || phase.Workers > d.plan.MaxQueueRequests)) {
		return phase, errors.New("invalid profiling phase supply or dataset")
	}
	start := time.Now()
	endFloor := start.Add(time.Duration(d.plan.OperatingPoints.PhaseDurationSeconds * 1e9))
	limit := d.plan.MaxLatencySamples * d.plan.Batch
	results := make(chan profilingCompletion, d.plan.MaxQueueRequests)
	alerts := make(chan error, 1)
	collector := profilingPhaseCollector{plan: d.plan, groups: make(map[uint64]native.Progress), requests: make(map[string]int)}
	var lags []float64
	var lastSent time.Time
	var firstErr error
	issued, inflight, known := 0, 0, 0
	stopping := false
	recordError := func(err error) {
		if err == nil {
			return
		}
		// Native allocation failure is the domain stop even if a scheduler or
		// another request failed earlier. Drain every issued request to see it.
		var failure *profilingLoadFailure
		if firstErr == nil || errors.As(err, &failure) && failure.FailureReason == "MemoryAllocationFailed" {
			firstErr = err
		}
		stopping = true
	}
	complete := func() bool {
		return !time.Now().Before(endFloor) && known >= d.plan.MinServiceSamples && issued%d.plan.Batch == 0
	}
	launch := func(scheduled time.Time) {
		index := issued
		issued++
		inflight++
		go func() { results <- d.send(ctx, phase, index, scheduled, alerts) }()
	}
	accept := func(result profilingCompletion) {
		inflight--
		if !result.sent.IsZero() && phase.Kind != "saturation" {
			lags = append(lags, result.lag)
			if result.sent.After(lastSent) {
				lastSent = result.sent
			}
		}
		if result.err != nil {
			recordError(result.err)
			if result.response.ID == "" {
				return
			}
		}
		newKnown, err := collector.add(result.row, result.response)
		recordError(err)
		if newKnown {
			known++
		}
	}
	if phase.Kind == "saturation" {
		for issued < phase.Workers && issued < limit {
			launch(time.Now())
		}
		for inflight > 0 {
			result := <-results
			accept(result)
			if !stopping && !complete() && issued < limit {
				launch(time.Now())
			}
		}
	} else {
		rng := rand.New(rand.NewPCG(0, 0))
		next := start
		advance := func() {
			next = next.Add(time.Duration(max(1, math.Round(rng.ExpFloat64()/phase.ArrivalRatePerSecond*1e9))))
		}
		advance()
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			if stopping || complete() || issued >= limit {
				break
			}
			if err := ctx.Err(); err != nil {
				recordError(err)
				break
			}
			now := time.Now()
			if !now.Before(next) {
				if inflight >= d.plan.MaxQueueRequests {
					recordError(errors.New("Poisson offered load exceeds frozen maxQueueRequests in-flight capacity; target rate was not reduced"))
				} else {
					launch(next)
					advance()
				}
				continue
			}
			select {
			case result := <-results:
				accept(result)
				continue
			case err := <-alerts:
				recordError(err)
				continue
			default:
			}
			// ponytail: spin at most the last 500us for sub-ms issue precision;
			// kernel pacing would be needed for a tighter shared-CPU ceiling.
			// Results and cancellation remain live throughout this window.
			wake := next.Add(-500 * time.Microsecond)
			if !now.Before(wake) {
				continue
			}
			if now.Before(endFloor) && endFloor.Before(wake) {
				wake = endFloor
			}
			timer.Reset(max(0, time.Until(wake)))
			select {
			case <-ctx.Done():
				recordError(ctx.Err())
			case err := <-alerts:
				recordError(err)
			case result := <-results:
				accept(result)
			case <-timer.C:
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		for inflight > 0 {
			accept(<-results)
		}
	}
	elapsed := time.Since(start).Seconds()
	phase.MeasurementStartedAt, phase.MeasurementEndedAt = start.UTC().Format(time.RFC3339Nano), start.Add(time.Duration(elapsed*1e9)).UTC().Format(time.RFC3339Nano)
	phase.MeasurementElapsedSeconds = elapsed
	phase.CompletedInputs = len(collector.rows)
	if phase.Kind != "saturation" {
		phase.IssueLag = profilingLagSummary(lags)
		phase.IssueLagToleranceMS = float64(profilingIssueLagTolerance) / 1e6
		if !lastSent.IsZero() {
			phase.IssueElapsedSeconds = lastSent.Sub(start).Seconds()
			phase.AchievedArrivalRatePerSecond = float64(phase.CompletedInputs) / phase.IssueElapsedSeconds
		}
	}
	if firstErr == nil && !complete() {
		firstErr = errors.New("latency sample budget exhausted before phase duration and known-gap minimum")
	}
	if firstErr == nil {
		phase.Requests, phase.NativeSamples, phase.LatencyMilliseconds, firstErr = collector.finish()
	}
	if firstErr != nil {
		failure := &profilingLoadFailure{Status: "Failed", Message: firstErr.Error(), cause: firstErr}
		var typed *profilingLoadFailure
		if errors.As(firstErr, &typed) {
			failure.FailureReason = typed.FailureReason
		}
		if phase.Kind != "saturation" {
			failure.IssueEvidence = &profilingIssueEvidence{ID: phase.ID, Kind: phase.Kind, Utilization: phase.Utilization, ArrivalRatePerSecond: phase.ArrivalRatePerSecond, AchievedArrivalRatePerSecond: phase.AchievedArrivalRatePerSecond, IssueElapsedSeconds: phase.IssueElapsedSeconds, IssueLagToleranceMS: phase.IssueLagToleranceMS, IssueLag: phase.IssueLag}
		}
		return phase, failure
	}
	return phase, nil
}

func profilingLagSummary(values []float64) *profilingrun.IssueLag {
	slices.Sort(values)
	result := &profilingrun.IssueLag{Count: len(values)}
	if len(values) == 0 {
		return result
	}
	percentile := func(fraction float64) float64 {
		index := float64(len(values)-1) * fraction
		low, high := int(math.Floor(index)), int(math.Ceil(index))
		return values[low] + (values[high]-values[low])*(index-float64(low))
	}
	for _, value := range values {
		result.Mean += value / float64(len(values))
	}
	result.Min, result.Max, result.P50, result.P95, result.P99 = values[0], values[len(values)-1], percentile(.5), percentile(.95), percentile(.99)
	return result
}

// Only one full proof per physical batch stays live. Each response is checked
// before its per-request record sheds repeated batch fields.
type profilingPhaseCollector struct {
	plan     profilingLoadPlan
	groups   map[uint64]native.Progress
	requests map[string]int
	rows     []profilingrun.PhaseRequest
}

func (c *profilingPhaseCollector) add(row profilingrun.PhaseRequest, response native.Response) (bool, error) {
	p := response.Progress
	pair, timing := p.ServiceSample, p.NativeHTTP
	if response.ID != row.RequestID || response.ModelName != c.plan.Model || !response.ExecutionComplete || len(response.Outputs) == 0 || p.RequestID != row.RequestID || p.WorkloadUID != c.plan.PodUID || p.Definition != c.plan.WorkloadResourceVersion || p.Phase != "finished" || !p.ExecutionComplete || p.BatchID == 0 || p.BatchSize != c.plan.Batch || p.Units < 1 || p.At.IsZero() || !profilingPositive(p.NativeElapsedMS) || p.NativeElapsedMS > row.LatencyMilliseconds {
		return false, errors.New("native response lacks correlated actual completion proof or nested client span")
	}
	if _, duplicate := c.requests[row.RequestID]; duplicate {
		return false, errors.New("duplicate native request ID")
	}
	if pair == nil || pair.TrialID != c.plan.PodUID || pair.ServiceDurationNanos <= 0 || pair.PrecedingIdleNanos != nil && *pair.PrecedingIdleNanos < 0 || len(p.BatchRequestIDs) != c.plan.Batch || !slices.Contains(p.BatchRequestIDs, row.RequestID) {
		return false, errors.New("native service pair/provenance or fixed-batch membership is invalid")
	}
	seen := make(map[string]bool, len(p.BatchRequestIDs))
	for _, id := range p.BatchRequestIDs {
		if id == "" || seen[id] {
			return false, errors.New("native batch membership contains duplicate or empty request IDs")
		}
		seen[id] = true
	}
	service := float64(pair.ServiceDurationNanos) / 1e6
	if !profilingNonnegative(p.QueueMS) || !profilingNonnegative(p.ServiceMS) || !profilingNonnegative(p.PreprocessMS) || !profilingPositive(p.BackendMS) || !profilingNonnegative(p.PostprocessMS) || !profilingNonnegative(p.FirstTokenMS) || p.PreprocessMS+p.BackendMS+p.PostprocessMS > service || service > p.NativeElapsedMS || p.ServiceMS < p.BackendMS || p.NativeElapsedMS < p.QueueMS+p.ServiceMS || p.ExecutorWaitNanos != nil && (*p.ExecutorWaitNanos < 0 || float64(*p.ExecutorWaitNanos)/1e6 > p.QueueMS) {
		return false, errors.New("native/client timing spans are not nested")
	}
	if p.BatchStartedAt == nil || p.BatchFinishedAt == nil || !p.BatchFinishedAt.After(*p.BatchStartedAt) || timing == nil || timing.HandlerEnteredAt.IsZero() || timing.ReceivedNanos == nil || timing.EnqueueNanos == nil || timing.FinishedNanos == nil || *timing.ReceivedNanos < 0 || *timing.EnqueueNanos < *timing.ReceivedNanos || *timing.FinishedNanos < *timing.EnqueueNanos || timing.HandlerEnteredAt.Add(time.Duration(*timing.EnqueueNanos)).After(*p.BatchStartedAt) {
		return false, errors.New("native batch timestamps or HTTP receive/enqueue/finish boundaries are invalid")
	}
	previous, exists := c.groups[p.BatchID]
	if exists && !profilingSameBatch(previous, p) {
		return false, errors.New("member responses disagree on native batch evidence")
	}
	if !exists {
		c.groups[p.BatchID] = p
	}
	row.Response = profilingrun.PhaseResponse{ModelName: response.ModelName, ID: response.ID, Outputs: response.Outputs, ExecutionComplete: response.ExecutionComplete, Progress: profilingrun.PhaseProgress{RequestID: p.RequestID, WorkloadUID: p.WorkloadUID, Definition: p.Definition, Phase: p.Phase, At: p.At, BatchID: p.BatchID, BatchSize: p.BatchSize, ProfileID: p.ProfileID, DecisionID: p.DecisionID, QueueMS: p.QueueMS, ExecutorWaitNanos: p.ExecutorWaitNanos, ServiceMS: p.ServiceMS, Units: p.Units, NativeElapsedMS: p.NativeElapsedMS, ExecutionComplete: p.ExecutionComplete, Outcome: p.Outcome, NativeHTTP: p.NativeHTTP, NativeDeferredAt: p.NativeDeferredAt}}
	c.requests[row.RequestID] = len(c.rows)
	c.rows = append(c.rows, row)
	return !exists && pair.PrecedingIdleNanos != nil, nil
}

func profilingSameBatch(a, b native.Progress) bool {
	pa, pb := a.ServiceSample, b.ServiceSample
	return slices.Equal(a.BatchRequestIDs, b.BatchRequestIDs) && pa.ServiceDurationNanos == pb.ServiceDurationNanos && pa.TrialID == pb.TrialID && (pa.PrecedingIdleNanos == nil && pb.PrecedingIdleNanos == nil || pa.PrecedingIdleNanos != nil && pb.PrecedingIdleNanos != nil && *pa.PrecedingIdleNanos == *pb.PrecedingIdleNanos) && a.PreprocessMS == b.PreprocessMS && a.BackendMS == b.BackendMS && a.PostprocessMS == b.PostprocessMS && a.FirstTokenMS == b.FirstTokenMS && a.BatchStartedAt.Equal(*b.BatchStartedAt) && a.BatchFinishedAt.Equal(*b.BatchFinishedAt)
}

func (c *profilingPhaseCollector) finish() ([]profilingrun.PhaseRequest, []profilingrun.NativeServiceSample, []float64, error) {
	ids := make([]uint64, 0, len(c.groups))
	for id := range c.groups {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	samples := make([]profilingrun.NativeServiceSample, 0, len(ids))
	latencies := make([]float64, 0, len(ids))
	covered := make(map[string]bool, len(c.rows))
	for _, id := range ids {
		p := c.groups[id]
		sample := profilingrun.NativeServiceSample{BatchID: id, BatchSize: c.plan.Batch, RequestIDs: p.BatchRequestIDs, ServiceSample: *p.ServiceSample, RunnerServiceMilliseconds: float64(p.ServiceSample.ServiceDurationNanos) / 1e6, PreprocessMilliseconds: p.PreprocessMS, BackendCallMilliseconds: p.BackendMS, PostprocessMilliseconds: p.PostprocessMS, FirstTokenMilliseconds: p.FirstTokenMS, StartedAt: *p.BatchStartedAt, FinishedAt: *p.BatchFinishedAt}
		var latency float64
		for _, requestID := range p.BatchRequestIDs {
			index, found := c.requests[requestID]
			if !found || covered[requestID] {
				return nil, nil, nil, errors.New("native batch membership is incomplete or duplicated")
			}
			row := c.rows[index]
			if row.Response.Progress.BatchID != id {
				return nil, nil, nil, errors.New("native batch membership disagrees with member batch IDs")
			}
			covered[requestID] = true
			sample.RecordIDs = append(sample.RecordIDs, row.RecordID)
			sample.Units = append(sample.Units, row.Response.Progress.Units)
			sample.NativeRequestMilliseconds = max(sample.NativeRequestMilliseconds, row.Response.Progress.NativeElapsedMS)
			latency = max(latency, row.LatencyMilliseconds)
			httpTiming := row.Response.Progress.NativeHTTP
			enqueued := httpTiming.HandlerEnteredAt.Add(time.Duration(*httpTiming.EnqueueNanos))
			if sample.EarliestEnqueuedAt.IsZero() || enqueued.Before(sample.EarliestEnqueuedAt) {
				sample.EarliestEnqueuedAt = enqueued
			}
			if enqueued.After(sample.LatestEnqueuedAt) {
				sample.LatestEnqueuedAt = enqueued
			}
		}
		samples, latencies = append(samples, sample), append(latencies, latency)
	}
	if len(covered) != len(c.rows) {
		return nil, nil, nil, errors.New("completed requests have no native batch sample")
	}
	// Request completion order need not equal issuance order. Fixed numeric
	// suffix order preserves the existing raw driver's request ordering.
	slices.SortFunc(c.rows, func(a, b profilingrun.PhaseRequest) int {
		if a.ArrivalAt.Before(b.ArrivalAt) {
			return -1
		}
		if a.ArrivalAt.After(b.ArrivalAt) {
			return 1
		}
		return strings.Compare(a.RequestID, b.RequestID)
	})
	return c.rows, samples, latencies, nil
}
