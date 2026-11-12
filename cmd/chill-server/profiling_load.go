package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	gateway "github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

var profilingResponseBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

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
	PostLoadDurationSeconds        float64   `json:"postLoadDurationSeconds"`
	RequestLifetimeSeconds         float64   `json:"requestLifetimeSeconds"`
	DeliveryMarginMilliseconds     float64   `json:"deliveryMarginMilliseconds"`
	FramedDecodedRequestUpperBytes int64     `json:"framedDecodedRequestUpperBytes"`
	QueueBytesCap                  int64     `json:"queueBytesCap"`
}

type profilingRecord struct {
	ID        string
	directory string
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
	ArrivalRatePerSecond         float64                `json:"arrivalRatePerSecond,omitempty"`
	AchievedArrivalRatePerSecond float64                `json:"achievedArrivalRatePerSecond"`
	IssueElapsedSeconds          float64                `json:"issueElapsedSecondsMonotonic"`
	IssueLag                     *profilingrun.IssueLag `json:"issueLagMs,omitempty"`
	ScheduledInputs              int                    `json:"scheduledInputs"`
	SentInputs                   int                    `json:"sentInputs"`
	CompletedInputs              int                    `json:"completedInputs"`
	OfferingStartedAt            time.Time              `json:"offeringStartedAt"`
	OfferingEndedAt              time.Time              `json:"offeringEndedAt"`
	DrainEndedAt                 time.Time              `json:"drainEndedAt"`
	ArrivalSeed                  string                 `json:"arrivalSeed,omitempty"`
	DriverGC                     *profilingrun.DriverGC `json:"driverGC"`
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
	return profilingLoad(ctx, *control, *dataset)
}

func profilingLoad(ctx context.Context, endpoint, dataset string) (err error) {
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
	driver := newProfilingLoadDriver(plan, records, endpoint)
	defer driver.close()
	_, err = driver.run(ctx)
	if err != nil {
		return err
	}
	file, err := driver.resultFile()
	if err != nil {
		return err
	}
	defer file.Close()
	return profilingControlCall(ctx, control, endpoint, http.MethodPost, "/measurement/load-result", file, nil)
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
	var body io.Reader
	if reader, ok := payload.(io.Reader); ok {
		body = reader
	} else if payload != nil {
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpoint, "/")+path, body)
	if err != nil {
		return err
	}
	if file, ok := payload.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		req.ContentLength = info.Size() - offset
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
	if !profilingPositive(points.PhaseDurationSeconds) || !profilingPositive(points.PostLoadDurationSeconds) || !profilingPositive(points.SparseMeanGapMilliseconds) || !profilingPositive(points.RequestLifetimeSeconds) || !profilingPositive(points.DeliveryMarginMilliseconds) || points.DeliveryMarginMilliseconds/1000 >= points.RequestLifetimeSeconds || points.FramedDecodedRequestUpperBytes < 1 || points.FramedDecodedRequestUpperBytes > native.MaxFrameBytes || points.QueueBytesCap < 1 || !profilingPositive(p.SLOPercentile) || p.SLOPercentile > 100 || p.SLOTargetLatencyNanos < 1 || len(points.Utilizations) == 0 {
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
		record := profilingRecord{ID: entry.Name(), directory: filepath.Join(directory, entry.Name())}
		var framed int64
		for _, field := range plan.Fields {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			info, err := os.Stat(filepath.Join(record.directory, field))
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Size() > plan.OperatingPoints.FramedDecodedRequestUpperBytes-framed-4 {
				return nil, errors.New("dataset record exceeds frozen framed decoded request upper bytes or is not a regular file")
			}
			framed += 4 + info.Size()
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, errors.New("profiling dataset contains no records")
	}
	return records, ctx.Err()
}

type profilingLoadDriver struct {
	plan          profilingLoadPlan
	records       []profilingRecord
	endpoint      string
	control       *http.Client
	native        *gateway.Client
	transport     *http.Transport
	inferURL      string
	dataDir       string
	evidenceDir   string
	stores        map[string]*profilingPhaseStore
	result        profilingLoadResult
	metadataBytes int64
	metadataValid bool
	cache         struct {
		sync.Mutex
		id     string
		inputs gateway.PreparedNativeInputs
	}
}

func newProfilingLoadDriver(plan profilingLoadPlan, records []profilingRecord, endpoint string) *profilingLoadDriver {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Native admission owns the explicit FIFO. HTTP pooling must not add a
	// second, unobserved dispatch queue behind those frozen native slots.
	transport.MaxIdleConnsPerHost = max(plan.MaxQueueRequests, 64)
	transport.MaxIdleConns = transport.MaxIdleConnsPerHost
	driver := &profilingLoadDriver{plan: plan, records: records, endpoint: endpoint, inferURL: strings.TrimRight(plan.NativeEndpoint, "/") + plan.InferPath, transport: transport, control: &http.Client{}, native: &gateway.Client{HTTPClient: &http.Client{Transport: transport, Timeout: time.Duration((plan.OperatingPoints.RequestLifetimeSeconds + plan.OperatingPoints.DeliveryMarginMilliseconds/1000) * 1e9)}}, result: profilingLoadResult{Status: "Succeeded", Phases: []profilingrun.Phase{}}}
	if len(records) > 0 {
		driver.dataDir = filepath.Dir(records[0].directory)
	}
	return driver
}

func (d *profilingLoadDriver) close() {
	d.transport.CloseIdleConnections()
	d.control.CloseIdleConnections()
	for _, store := range d.stores {
		_ = store.close()
	}
	if d.evidenceDir != "" {
		_ = os.RemoveAll(d.evidenceDir)
	}
}

func (d *profilingLoadDriver) run(ctx context.Context) (profilingLoadResult, error) {
	result := &d.result
	_, err := d.measuredPhase(ctx, profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 1000 / d.plan.OperatingPoints.SparseMeanGapMilliseconds})
	if err != nil {
		return *result, err
	}
	supply, err := os.CreateTemp(d.evidenceDir, "supply-")
	if err != nil {
		return *result, err
	}
	defer supply.Close()
	if _, err := io.WriteString(supply, `{"nativeSamples":`); err != nil {
		return *result, err
	}
	if err := d.stores["sparse"].samples.write(supply); err != nil {
		return *result, err
	}
	if _, err := io.WriteString(supply, "}"); err != nil {
		return *result, err
	}
	if _, err := supply.Seek(0, io.SeekStart); err != nil {
		return *result, err
	}
	if err := profilingControlCall(ctx, d.control, d.endpoint, http.MethodPost, "/measurement/supply", supply, &result.SupplySizing); err != nil {
		return *result, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, result.SupplySizing); err != nil {
		return *result, err
	}
	result.SupplySizing = compact.Bytes()
	d.metadataValid = false
	if err := d.checkResultBudget(); err != nil {
		return *result, err
	}
	var sizing struct {
		Workers int `json:"workers"`
	}
	if err := json.Unmarshal(result.SupplySizing, &sizing); err != nil {
		return *result, err
	}
	if sizing.Workers < d.plan.Batch || sizing.Workers > 64 || sizing.Workers > d.plan.MaxQueueRequests {
		return *result, errors.New("native saturation sizing exceeds frozen request/worker cap or fixed batch")
	}
	saturation, err := d.measuredPhase(ctx, profilingrun.Phase{ID: "saturation", Kind: "saturation", Workers: sizing.Workers})
	if err != nil {
		return *result, err
	}
	throughput := float64(saturation.CompletedInputs) / saturation.MeasurementElapsedSeconds
	if !profilingPositive(throughput) {
		return *result, errors.New("measured saturation input throughput is invalid")
	}
	for index, utilization := range d.plan.OperatingPoints.Utilizations {
		_, err := d.measuredPhase(ctx, profilingrun.Phase{ID: fmt.Sprintf("utilization-%d", index), Kind: "utilization", Utilization: utilization, ArrivalRatePerSecond: utilization * throughput})
		if err != nil {
			return *result, err
		}
	}
	return *result, d.checkResultBudget()
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
	return phase, errors.Join(err, d.updatePhase(phase))
}

type profilingCompletion struct {
	row        profilingrun.PhaseRequest
	response   native.Response
	dispatched time.Time
	completed  time.Time
	lag        float64
	err        error
}

func (d *profilingLoadDriver) send(ctx context.Context, spec profilingrun.Phase, index int, scheduled time.Time, issued, completed func()) profilingCompletion {
	record := d.records[index%len(d.records)]
	inputs, err := d.recordInputs(ctx, record)
	if err != nil {
		issued()
		if completed != nil {
			completed()
		}
		return profilingCompletion{err: err}
	}
	var idBuffer [64]byte
	id := string(strconv.AppendInt(append(append(idBuffer[:0], spec.ID...), '-'), int64(index), 10))
	points := d.plan.OperatingPoints
	response := profilingResponseBuffers.Get().(*bytes.Buffer)
	defer profilingResponseBuffers.Put(response)
	// The gateway's client clock starts at arrival. Admission-slot waiting is
	// sender elapsed time and belongs to the end-to-end client latency.
	sent := time.Now()
	issued()
	request := native.Request{ID: id, Context: native.RequestContext{RequestID: id, WorkloadUID: d.plan.PodUID, Definition: d.plan.WorkloadResourceVersion, AdmissionPolicy: "Delay", ReceivedAt: scheduled.UTC(), SenderElapsedMS: float64(sent.Sub(scheduled)) / 1e6, CompletionMarginMS: points.DeliveryMarginMilliseconds, SLO: native.SLO{Percentile: d.plan.SLOPercentile, TargetMS: float64(d.plan.SLOTargetLatencyNanos) / 1e6}, LifetimeMS: points.RequestLifetimeSeconds * 1000}}
	status, err := d.native.InferNative(ctx, d.inferURL, request, inputs, response)
	// HTTP completion releases the outstanding slot before JSON decoding and
	// native-proof collection, neither of which may reshape Poisson issuance.
	if completed != nil {
		completed()
	}
	raw := response.Bytes()
	finished := time.Now()
	result := profilingCompletion{dispatched: sent, completed: finished, lag: max(0, float64(sent.Sub(scheduled))/1e6), err: err}
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
	result.row = profilingrun.PhaseRequest{RequestID: id, RecordID: record.ID, ScheduledAt: scheduled.UTC(), DispatchedAt: sent.UTC(), CompletedAt: finished.UTC(), LatencyMilliseconds: float64(finished.Sub(scheduled)) / 1e6}
	return result
}

func (d *profilingLoadDriver) runPhase(ctx context.Context, phase profilingrun.Phase) (profilingrun.Phase, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if len(d.records) == 0 || (phase.Kind != "saturation" && !profilingPositive(phase.ArrivalRatePerSecond)) || (phase.Kind == "saturation" && (phase.Workers < d.plan.Batch || phase.Workers > d.plan.MaxQueueRequests)) {
		return phase, errors.New("invalid profiling phase supply or dataset")
	}
	store, err := d.openPhaseStore(phase.ID)
	if err != nil {
		return phase, err
	}
	limit := d.plan.MaxLatencySamples * d.plan.Batch
	results := make(chan profilingCompletion, d.plan.MaxQueueRequests)
	stop := make(chan struct{})
	collector := profilingPhaseCollector{plan: d.plan, store: store, groups: make(map[uint64]*profilingBatchGroup), requests: make(map[string]bool), completed: make(map[uint64]bool), checkBudget: d.checkResultBudget}
	var lags []float64
	var memoryBefore, memoryAfter runtime.MemStats
	runtime.ReadMemStats(&memoryBefore)
	start := time.Now()
	endFloor := start.Add(time.Duration(d.plan.OperatingPoints.PhaseDurationSeconds * 1e9))
	phase.MeasurementStartedAt = start.UTC().Format(time.RFC3339Nano)
	phase.OfferingStartedAt = start.UTC()
	if err := d.updatePhase(phase); err != nil {
		return phase, err
	}
	var firstErr error
	var known atomic.Int64
	var requests, sending sync.WaitGroup
	issued, sent := 0, 0
	var lastScheduled, lastCompleted time.Time
	sendSpec := profilingrun.Phase{ID: phase.ID}
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
		if errors.Is(err, errProfilingResultBudget) {
			cancel()
		}
		if !stopping {
			close(stop)
			stopping = true
		}
	}
	complete := func() bool {
		return !time.Now().Before(endFloor) && known.Load() >= int64(d.plan.MinServiceSamples) && issued%d.plan.Batch == 0
	}
	launch := func(index int, scheduled time.Time, completed func()) {
		requests.Add(1)
		sending.Add(1)
		go func() {
			defer requests.Done()
			result := d.send(ctx, sendSpec, index, scheduled, sending.Done, completed)
			results <- result
		}()
	}
	accept := func(result profilingCompletion) {
		if !result.dispatched.IsZero() {
			sent++
			if phase.Kind != "saturation" {
				lags = append(lags, result.lag)
			}
		}
		if result.completed.After(lastCompleted) {
			lastCompleted = result.completed
		}
		if result.err != nil {
			recordError(result.err)
			if result.response.ID == "" {
				return
			}
		}
		if errors.Is(firstErr, errProfilingResultBudget) {
			return
		}
		newKnown, err := collector.add(result.row, result.response)
		recordError(err)
		if err == nil {
			phase.CompletedInputs = collector.count
			recordError(d.updatePhase(phase))
		}
		if newKnown {
			known.Add(1)
		}
	}
	if phase.Kind == "saturation" {
		pending := min(phase.Workers, limit)
		for range pending {
			lastScheduled = time.Now()
			launch(issued, lastScheduled, nil)
			issued++
		}
		finishedIssuing := false
		for pending > 0 {
			accept(<-results)
			pending--
			if !stopping && !complete() && issued < limit {
				lastScheduled = time.Now()
				launch(issued, lastScheduled, nil)
				issued++
				pending++
			} else if !finishedIssuing {
				sending.Wait()
				runtime.ReadMemStats(&memoryAfter)
				finishedIssuing = true
			}
		}
		requests.Wait()
		if !finishedIssuing {
			runtime.ReadMemStats(&memoryAfter)
		}
	} else {
		phase.ArrivalSeed = d.plan.PodUID + "/" + phase.ID
		hash := fnv.New128a()
		_, _ = io.WriteString(hash, phase.ArrivalSeed)
		var seed [16]byte
		sum := hash.Sum(seed[:0])
		rng := rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum[:8]), binary.BigEndian.Uint64(sum[8:])))
		type producerResult struct {
			scheduled int
			last      time.Time
			err       error
		}
		producerDone := make(chan producerResult, 1)
		type arrival struct {
			index     int
			scheduled time.Time
		}
		queue := struct {
			sync.Mutex
			items []arrival
			head  int
			done  bool
		}{}
		wakeDispatcher := make(chan struct{}, 1)
		notify := func() {
			select {
			case wakeDispatcher <- struct{}{}:
			default:
			}
		}
		slots := make(chan struct{}, d.plan.MaxQueueRequests)
		go func() {
			produced := producerResult{}
			next := start
			advance := func() {
				next = next.Add(time.Duration(max(1, math.Round(rng.ExpFloat64()/phase.ArrivalRatePerSecond*1e9))))
			}
			advance()
			timer := time.NewTimer(0)
			defer timer.Stop()
			var cutoff time.Time
			for {
				select {
				case <-stop:
					goto finished
				default:
				}
				if err := ctx.Err(); err != nil {
					produced.err = err
					break
				}
				now := time.Now()
				if cutoff.IsZero() && !now.Before(endFloor) && known.Load() >= int64(d.plan.MinServiceSamples) {
					cutoff = now
				}
				// Catch up all arrivals through the cutoff, including scheduler
				// gaps. Extend only to complete a fixed physical batch.
				if !cutoff.IsZero() && next.After(cutoff) && issued%d.plan.Batch == 0 {
					break
				}
				if issued >= limit {
					produced.err = errors.New("latency sample budget exhausted before phase duration and known-gap minimum")
					break
				}
				if !now.Before(next) {
					produced.scheduled++
					queue.Lock()
					queue.items = append(queue.items, arrival{index: issued, scheduled: next})
					queue.Unlock()
					issued++
					notify()
					produced.last = next
					advance()
					continue
				}
				// ponytail: spin at most the last 500us for sub-ms issue precision;
				// kernel pacing would be needed for a tighter shared-CPU ceiling.
				// Proof collection runs separately and cannot pace this producer.
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
					produced.err = ctx.Err()
				case <-stop:
				case <-timer.C:
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				if produced.err != nil {
					break
				}
			}
		finished:
			queue.Lock()
			queue.done = true
			queue.Unlock()
			notify()
			producerDone <- produced
		}()
		go func() {
			defer func() {
				sending.Wait()
				runtime.ReadMemStats(&memoryAfter)
				requests.Wait()
				close(results)
			}()
			for {
				queue.Lock()
				if queue.head == len(queue.items) {
					done := queue.done
					queue.Unlock()
					if done {
						return
					}
					select {
					case <-wakeDispatcher:
					case <-stop:
						return
					case <-ctx.Done():
						return
					}
					continue
				}
				next := queue.items[queue.head]
				queue.head++
				if queue.head == len(queue.items) {
					queue.items, queue.head = queue.items[:0], 0
				}
				queue.Unlock()
				select {
				case slots <- struct{}{}:
				case <-stop:
					return
				case <-ctx.Done():
					return
				}
				select {
				case <-stop:
					<-slots
					return
				case <-ctx.Done():
					<-slots
					return
				default:
				}
				launch(next.index, next.scheduled, func() { <-slots })
				// Preserve FIFO dispatch even when record preparation or a
				// pre-send allocation is delayed. The arrival clock is separate.
				sending.Wait()
			}
		}()
		for result := range results {
			accept(result)
		}
		produced := <-producerDone
		recordError(produced.err)
		lastScheduled = produced.last
		phase.IssueLag = profilingLagSummary(lags)
	}
	phase.ScheduledInputs, phase.SentInputs = issued, sent
	phase.OfferingEndedAt, phase.DrainEndedAt = lastScheduled.UTC(), lastCompleted.UTC()
	if lastScheduled.IsZero() {
		phase.OfferingEndedAt = phase.OfferingStartedAt
	} else {
		phase.IssueElapsedSeconds = lastScheduled.Sub(start).Seconds()
		phase.AchievedArrivalRatePerSecond = float64(phase.ScheduledInputs) / phase.IssueElapsedSeconds
	}
	if lastCompleted.IsZero() {
		phase.DrainEndedAt = phase.OfferingStartedAt
	}
	elapsed := time.Since(start).Seconds()
	phase.MeasurementStartedAt, phase.MeasurementEndedAt = start.UTC().Format(time.RFC3339Nano), start.Add(time.Duration(elapsed*1e9)).UTC().Format(time.RFC3339Nano)
	phase.MeasurementElapsedSeconds = elapsed
	phase.CompletedInputs = collector.count
	phase.DriverGC = profilingDriverGCSummary(memoryBefore, memoryAfter)
	if firstErr == nil && !complete() {
		firstErr = errors.New("latency sample budget exhausted before phase duration and known-gap minimum")
	}
	if firstErr == nil && (phase.ScheduledInputs != phase.SentInputs || phase.SentInputs != phase.CompletedInputs) {
		firstErr = errors.New("scheduled, dispatched and completed input counts differ")
	}
	if firstErr == nil {
		firstErr = errors.Join(collector.finish(), d.updatePhase(phase))
	}
	if firstErr != nil {
		failure := &profilingLoadFailure{Status: "Failed", Message: firstErr.Error(), cause: firstErr}
		var typed *profilingLoadFailure
		if errors.As(firstErr, &typed) {
			failure.FailureReason = typed.FailureReason
		}
		failure.IssueEvidence = &profilingIssueEvidence{ID: phase.ID, Kind: phase.Kind, Utilization: phase.Utilization, ArrivalRatePerSecond: phase.ArrivalRatePerSecond, AchievedArrivalRatePerSecond: phase.AchievedArrivalRatePerSecond, IssueElapsedSeconds: phase.IssueElapsedSeconds, IssueLag: phase.IssueLag, ScheduledInputs: phase.ScheduledInputs, SentInputs: phase.SentInputs, CompletedInputs: phase.CompletedInputs, OfferingStartedAt: phase.OfferingStartedAt, OfferingEndedAt: phase.OfferingEndedAt, DrainEndedAt: phase.DrainEndedAt, ArrivalSeed: phase.ArrivalSeed, DriverGC: phase.DriverGC}
		return phase, failure
	}
	return phase, nil
}

func profilingDriverGCSummary(before, after runtime.MemStats) *profilingrun.DriverGC {
	result := &profilingrun.DriverGC{Cycles: after.NumGC - before.NumGC, PauseTotalMilliseconds: float64(after.PauseTotalNs-before.PauseTotalNs) / 1e6}
	for cycle := uint32(0); cycle < min(result.Cycles, uint32(len(after.PauseNs))); cycle++ {
		pause := after.PauseNs[(after.NumGC-1-cycle)%uint32(len(after.PauseNs))]
		result.MaxPauseMilliseconds = max(result.MaxPauseMilliseconds, float64(pause)/1e6)
	}
	return result
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

// Only unfinished physical batches retain proof and member metadata in memory.
// Outputs are appended to the data volume immediately after proof validation.
type profilingPhaseCollector struct {
	plan        profilingLoadPlan
	store       *profilingPhaseStore
	groups      map[uint64]*profilingBatchGroup
	requests    map[string]bool
	completed   map[uint64]bool
	count       int
	checkBudget func() error
}

type profilingBatchGroup struct {
	proof native.Progress
	rows  map[string]profilingrun.PhaseRequest
}

func (c *profilingPhaseCollector) add(row profilingrun.PhaseRequest, response native.Response) (bool, error) {
	p := response.Progress
	pair, timing := p.ServiceSample, p.NativeHTTP
	if response.ID != row.RequestID || response.ModelName != c.plan.Model || !response.ExecutionComplete || len(response.Outputs) == 0 || p.RequestID != row.RequestID || p.WorkloadUID != c.plan.PodUID || p.Definition != c.plan.WorkloadResourceVersion || p.Phase != "finished" || !p.ExecutionComplete || p.BatchID == 0 || p.BatchSize != c.plan.Batch || p.Units < 1 || p.At.IsZero() || !profilingPositive(p.NativeElapsedMS) || p.NativeElapsedMS > row.LatencyMilliseconds || p.NativeElapsedMS > float64(row.CompletedAt.Sub(row.DispatchedAt))/1e6 {
		return false, errors.New("native response lacks correlated actual completion proof or nested client span")
	}
	if _, duplicate := c.requests[row.RequestID]; duplicate {
		return false, errors.New("duplicate native request ID")
	}
	if pair == nil || pair.TrialID != c.plan.PodUID || pair.ServiceDurationNanos <= 0 || pair.PrecedingIdleNanos != nil && *pair.PrecedingIdleNanos < 0 || len(p.BatchRequestIDs) != c.plan.Batch || !slices.Contains(p.BatchRequestIDs, row.RequestID) {
		return false, errors.New("native service pair/provenance or fixed-batch membership is invalid")
	}
	previous, exists := c.groups[p.BatchID]
	if c.completed[p.BatchID] {
		return false, errors.New("native batch ID was reused after completion")
	}
	if !exists {
		seen := make(map[string]bool, len(p.BatchRequestIDs))
		for _, id := range p.BatchRequestIDs {
			if id == "" || seen[id] {
				return false, errors.New("native batch membership contains duplicate or empty request IDs")
			}
			seen[id] = true
		}
	}
	service := float64(pair.ServiceDurationNanos) / 1e6
	if !profilingNonnegative(p.QueueMS) || !profilingNonnegative(p.ServiceMS) || !profilingNonnegative(p.PreprocessMS) || !profilingPositive(p.BackendMS) || !profilingNonnegative(p.PostprocessMS) || !profilingNonnegative(p.FirstTokenMS) || p.PreprocessMS+p.BackendMS+p.PostprocessMS > service || service > p.NativeElapsedMS || p.ServiceMS < p.BackendMS || p.NativeElapsedMS < p.QueueMS+p.ServiceMS || p.ExecutorWaitNanos != nil && (*p.ExecutorWaitNanos < 0 || float64(*p.ExecutorWaitNanos)/1e6 > p.QueueMS) {
		return false, errors.New("native/client timing spans are not nested")
	}
	if p.BatchStartedAt == nil || p.BatchFinishedAt == nil || !p.BatchFinishedAt.After(*p.BatchStartedAt) || timing == nil || timing.HandlerEnteredAt.IsZero() || timing.ReceivedNanos == nil || timing.EnqueueNanos == nil || timing.FinishedNanos == nil || *timing.ReceivedNanos < 0 || *timing.EnqueueNanos < *timing.ReceivedNanos || *timing.FinishedNanos < *timing.EnqueueNanos || timing.HandlerEnteredAt.Add(time.Duration(*timing.EnqueueNanos)).After(*p.BatchStartedAt) {
		return false, errors.New("native batch timestamps or HTTP receive/enqueue/finish boundaries are invalid")
	}
	if exists && !profilingSameBatch(previous.proof, p) {
		return false, errors.New("member responses disagree on native batch evidence")
	}
	if !exists {
		previous = &profilingBatchGroup{proof: p, rows: make(map[string]profilingrun.PhaseRequest, c.plan.Batch)}
		c.groups[p.BatchID] = previous
	}
	row.Response = profilingrun.PhaseResponse{ModelName: response.ModelName, ID: response.ID, Outputs: response.Outputs, ExecutionComplete: response.ExecutionComplete, Progress: profilingrun.PhaseProgress{RequestID: p.RequestID, WorkloadUID: p.WorkloadUID, Definition: p.Definition, Phase: p.Phase, At: p.At, BatchID: p.BatchID, BatchSize: p.BatchSize, ProfileID: p.ProfileID, DecisionID: p.DecisionID, QueueMS: p.QueueMS, ExecutorWaitNanos: p.ExecutorWaitNanos, ServiceMS: p.ServiceMS, Units: p.Units, NativeElapsedMS: p.NativeElapsedMS, ExecutionComplete: p.ExecutionComplete, Outcome: p.Outcome, NativeHTTP: p.NativeHTTP, NativeDeferredAt: p.NativeDeferredAt}}
	if err := c.store.requests.append(row, profilingJSONEntry{at: row.ScheduledAt, id: row.RequestID}); err != nil {
		return false, err
	}
	if err := c.checkBudget(); err != nil {
		return false, err
	}
	c.count++
	c.requests[row.RequestID] = true
	row.Response.Outputs = nil
	previous.rows[row.RequestID] = row
	if len(previous.rows) == c.plan.Batch {
		if err := c.completeBatch(p.BatchID, previous); err != nil {
			return false, err
		}
		delete(c.groups, p.BatchID)
		c.completed[p.BatchID] = true
	}
	return !exists && pair.PrecedingIdleNanos != nil, nil
}

func profilingSameBatch(a, b native.Progress) bool {
	pa, pb := a.ServiceSample, b.ServiceSample
	return slices.Equal(a.BatchRequestIDs, b.BatchRequestIDs) && pa.ServiceDurationNanos == pb.ServiceDurationNanos && pa.TrialID == pb.TrialID && (pa.PrecedingIdleNanos == nil && pb.PrecedingIdleNanos == nil || pa.PrecedingIdleNanos != nil && pb.PrecedingIdleNanos != nil && *pa.PrecedingIdleNanos == *pb.PrecedingIdleNanos) && a.PreprocessMS == b.PreprocessMS && a.BackendMS == b.BackendMS && a.PostprocessMS == b.PostprocessMS && a.FirstTokenMS == b.FirstTokenMS && a.BatchStartedAt.Equal(*b.BatchStartedAt) && a.BatchFinishedAt.Equal(*b.BatchFinishedAt)
}

func (c *profilingPhaseCollector) completeBatch(id uint64, group *profilingBatchGroup) error {
	p := group.proof
	sample := profilingrun.NativeServiceSample{BatchID: id, BatchSize: c.plan.Batch, RequestIDs: p.BatchRequestIDs, ServiceSample: *p.ServiceSample, RunnerServiceMilliseconds: float64(p.ServiceSample.ServiceDurationNanos) / 1e6, PreprocessMilliseconds: p.PreprocessMS, BackendCallMilliseconds: p.BackendMS, PostprocessMilliseconds: p.PostprocessMS, FirstTokenMilliseconds: p.FirstTokenMS, StartedAt: *p.BatchStartedAt, FinishedAt: *p.BatchFinishedAt}
	var latency float64
	for _, requestID := range p.BatchRequestIDs {
		row, found := group.rows[requestID]
		if !found {
			return errors.New("native batch membership is incomplete or duplicated")
		}
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
	if err := c.store.samples.append(sample, profilingJSONEntry{batch: id}); err != nil {
		return err
	}
	if err := c.store.latencies.append(latency, profilingJSONEntry{batch: id}); err != nil {
		return err
	}
	return c.checkBudget()
}

func (c *profilingPhaseCollector) finish() error {
	if len(c.groups) != 0 || c.count != len(c.completed)*c.plan.Batch {
		return errors.New("native batch membership is incomplete or duplicated")
	}
	c.store.sort()
	c.groups, c.requests, c.completed = nil, nil, nil
	return nil
}
