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
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	gateway "github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

// session-load drives the load levels of one batch size of a profile
// measurement session against a native instance's Service address. It keeps
// counters only and reports one level table as the container termination
// message.

const (
	sessionTerminationPath = "/dev/termination-log"
	// The kubelet truncates a container termination message above 4096 bytes.
	sessionTerminationBytes = 3072
	sessionErrorBytes       = 1024
	sessionDetailBytes      = 512

	sessionSaturation = "saturation"
	sessionMid        = "mid"
	sessionSparse     = "sparse"

	sessionSamplesReached         = "SamplesReached"
	sessionServiceOverLifetime    = "BatchServiceExceedsLifetime"
	sessionAllocationBeforeBatch  = "AllocationFailedBeforeAnyBatch"
	sessionAllocationAfterBatch   = "AllocationFailedAfterCompletedBatch"
	sessionFailed                 = "Failed"
	sessionRequestLifetimeExpired = "RequestLifetimeExpired"
)

type sessionLoadConfig struct {
	endpoint, model, dataset, session, definition    string
	fields                                           []string
	batch, minSamples, maxQueueRequests              int
	maxQueueBytes, requestUpperBytes                 int64
	sloPercentile, midRatio                          float64
	sloTarget, lifetime, margin                      time.Duration
	meterLag, powerWindow, sparseMeanGap, levelLimit time.Duration
	sparse                                           bool
}

// workloadUID names a level's requests. Native batches only requests of one
// workload and records the identifier in every progress record, so the
// session selects a level's executor records by it.
func (c sessionLoadConfig) workloadUID(kind string) string {
	return c.session + "/" + strconv.Itoa(c.batch) + "/" + kind
}

// sessionLevel is one row of the level table.
type sessionLevel struct {
	Kind                    string    `json:"kind"`
	WorkloadUID             string    `json:"workloadUID"`
	StartedAt               time.Time `json:"startedAt"`
	EndedAt                 time.Time `json:"endedAt"`
	SentInputs              int       `json:"sentInputs"`
	CompletedInputs         int       `json:"completedInputs"`
	CompletedBatches        int       `json:"completedBatches"`
	Outstanding             int       `json:"outstanding,omitempty"`
	TargetInputsPerSecond   float64   `json:"targetInputsPerSecond,omitempty"`
	AchievedInputsPerSecond float64   `json:"achievedInputsPerSecond"`
	PredictedSeconds        float64   `json:"predictedSeconds,omitempty"`
	LimitSeconds            float64   `json:"limitSeconds,omitempty"`
	EndReason               string    `json:"endReason"`
	Detail                  string    `json:"detail,omitempty"`
}

func sessionLoadCommand(args []string, terminationPath string, stdout io.Writer) error {
	config, err := parseSessionLoad(args)
	var levels []sessionLevel
	if err == nil {
		levels, err = runSessionLoad(context.Background(), config)
	}
	message, encodeErr := sessionTerminationMessage(levels, err)
	if encodeErr != nil {
		return errors.Join(err, encodeErr)
	}
	_, _ = stdout.Write(append(message, '\n'))
	return errors.Join(err, os.WriteFile(terminationPath, message, 0o600))
}

func parseSessionLoad(args []string) (sessionLoadConfig, error) {
	var c sessionLoadConfig
	flags := flag.NewFlagSet("session-load", flag.ContinueOnError)
	flags.StringVar(&c.endpoint, "native-endpoint", "", "native HTTP endpoint of the instance Service")
	flags.StringVar(&c.model, "model", "", "native model name")
	fields := flags.String("fields", "", "comma-separated dataset field file names")
	flags.StringVar(&c.dataset, "dataset-dir", "", "raw record dataset directory")
	flags.IntVar(&c.batch, "batch", 0, "fixed native batch size")
	flags.StringVar(&c.session, "session", "", "session identifier; requests carry <session>/<batch>/<level>")
	flags.StringVar(&c.definition, "definition", "", "request workload definition")
	flags.Float64Var(&c.sloPercentile, "slo-percentile", 0, "service latency percentile")
	flags.DurationVar(&c.sloTarget, "slo-target", 0, "service latency target")
	flags.DurationVar(&c.lifetime, "request-lifetime", 0, "native request lifetime")
	flags.DurationVar(&c.margin, "delivery-margin", 0, "native completion delivery margin")
	flags.IntVar(&c.minSamples, "min-samples", 0, "minimum batch service samples per level")
	flags.DurationVar(&c.meterLag, "meter-lag", 0, "power meter lag discarded at level start")
	flags.DurationVar(&c.powerWindow, "min-power-window", 0, "minimum power observation per level")
	flags.Float64Var(&c.midRatio, "mid-ratio", 0, "mid level arrival rate as a ratio of saturation throughput")
	flags.BoolVar(&c.sparse, "sparse", false, "run the sparse level after mid")
	flags.DurationVar(&c.sparseMeanGap, "sparse-mean-gap", 0, "sparse level mean idle gap")
	flags.DurationVar(&c.levelLimit, "level-limit", 0, "longest predicted duration of one level")
	flags.IntVar(&c.maxQueueRequests, "max-queue-requests", 0, "native admission request limit")
	flags.Int64Var(&c.maxQueueBytes, "max-queue-bytes", 0, "native admission byte limit")
	flags.Int64Var(&c.requestUpperBytes, "request-upper-bytes", 0, "framed decoded bytes upper bound of one request")
	if err := flags.Parse(args); err != nil {
		return c, err
	}
	c.fields = strings.Split(*fields, ",")
	endpoint, err := url.Parse(c.endpoint)
	switch {
	case flags.NArg() != 0 || err != nil || endpoint.Scheme != "http" || endpoint.Host == "" || c.model == "" ||
		*fields == "" || c.dataset == "" || c.definition == "":
		return c, errors.New("session-load requires an http --native-endpoint, --model, --fields, --dataset-dir " +
			"and --definition")
	case c.batch < 1 || c.minSamples < 1 || c.requestUpperBytes < 1 || c.maxQueueRequests < 1 || c.maxQueueBytes < 1:
		return c, errors.New("session-load requires positive batch, sample, request and queue counts")
	case c.levelLimit <= 0:
		return c, errors.New("session-load requires a positive --level-limit")
	}
	return c, nil
}

type sessionLoad struct {
	config    sessionLoadConfig
	records   []profilingRecord
	source    profilingRecordSource
	client    *gateway.Client
	transport *http.Transport
	inferURL  string
	// sent indexes dataset records over the run; batches counts completed
	// batches of earlier levels.
	sent, batches int
}

func newSessionLoad(c sessionLoadConfig, records []profilingRecord) *sessionLoad {
	client, transport := newNativeLoadClient(c.maxQueueRequests, c.lifetime+c.margin)
	s := &sessionLoad{
		config: c, records: records, client: client, transport: transport,
		inferURL: strings.TrimRight(c.endpoint, "/") + "/v2/models/" + url.PathEscape(c.model) + "/infer",
	}
	s.source.fields, s.source.upperBytes = c.fields, c.requestUpperBytes
	return s
}

// runSessionLoad runs saturation, mid and, when asked, sparse. A mid lifetime
// expiry preserves its row and allows sparse to run after physical idle.
func runSessionLoad(ctx context.Context, c sessionLoadConfig) ([]sessionLevel, error) {
	records, err := readProfilingRecords(ctx, c.dataset, c.fields, c.requestUpperBytes)
	if err != nil {
		return nil, err
	}
	s := newSessionLoad(c, records)
	defer s.transport.CloseIdleConnections()
	kinds := []string{sessionSaturation, sessionMid}
	if c.sparse {
		kinds = append(kinds, sessionSparse)
	}
	levels := make([]sessionLevel, 0, len(kinds))
	for _, kind := range kinds {
		var rate float64
		if kind == sessionMid {
			if rate = c.midRatio * levels[0].AchievedInputsPerSecond; rate <= 0 {
				return levels, errors.New("saturation level completed no input after the meter lag")
			}
		}
		level, err := s.level(ctx, kind, rate)
		if err == nil && level.EndReason == sessionRequestLifetimeExpired {
			err = s.waitIdle(ctx, level.Detail)
			level.EndedAt = time.Now().UTC()
		}
		levels = append(levels, level)
		endedEarly := level.EndReason == sessionRequestLifetimeExpired
		if err != nil || level.EndReason != sessionSamplesReached && (kind == sessionSaturation || !endedEarly) {
			return levels, err
		}
	}
	return levels, nil
}

// waitIdle observes physical completion: an expired HTTP request may still
// belong to an executing, non-preemptible native batch.
func (s *sessionLoad) waitIdle(parent context.Context, detail string) error {
	ctx, cancel := context.WithTimeout(parent, s.config.levelLimit)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	spec := gateway.Spec{Endpoint: s.config.endpoint, ModelName: s.config.model}
	for {
		state, err := s.client.State(ctx, spec)
		if err != nil {
			return fmt.Errorf("%s: read native state while waiting for idle: %w", detail, err)
		}
		if len(state.Requests) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: native executor did not become idle within %s (%d requests remain): %w",
				detail, s.config.levelLimit, len(state.Requests), ctx.Err())
		case <-ticker.C:
		}
	}
}

// level collects the minimum service samples and power window within the
// configured actual time limit. It records a duration prediction from the
// first completed batch, or from the measured saturation rate for mid.
func (s *sessionLoad) level(parent context.Context, kind string, rate float64) (sessionLevel, error) {
	ctx, cancel := context.WithTimeout(parent, s.config.levelLimit)
	defer cancel()
	c := s.config
	start := time.Now()
	l := &sessionLevelRun{
		s: s, ctx: ctx, cancel: cancel, rate: rate, counter: newSessionCounter(),
		row: sessionLevel{
			Kind: kind, WorkloadUID: c.workloadUID(kind), StartedAt: start.UTC(), TargetInputsPerSecond: rate,
			LimitSeconds: c.levelLimit.Seconds(),
		},
		request: loadRequestContext{
			workloadUID: c.workloadUID(kind), definition: c.definition,
			slo:      native.SLO{Percentile: c.sloPercentile, TargetMS: float64(c.sloTarget) / 1e6},
			marginMS: float64(c.margin) / 1e6, lifetimeMS: float64(c.lifetime) / 1e6,
		},
		lagEnd: start.Add(c.meterLag), windowEnd: start.Add(c.meterLag + c.powerWindow),
		rng: arrivalRNG(c.workloadUID(kind)), next: start, results: make(chan sessionResult, c.maxQueueRequests),
	}
	switch kind {
	case sessionSaturation:
		// One batch executing and one full batch waiting saturate a fixed-batch executor.
		l.limit = 2 * c.batch
		l.row.Outstanding = l.limit
	case sessionMid:
		l.limit = int(min(int64(c.maxQueueRequests), c.maxQueueBytes/c.requestUpperBytes))
		l.next = start.Add(l.arrivalGap())
		l.predict(time.Duration(float64(c.batch) / rate * 1e9))
	case sessionSparse:
		l.limit = c.batch
	}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		now := time.Now()
		if l.row.EndReason == "" && l.counter.samples >= c.minSamples && !now.Before(l.windowEnd) {
			l.row.EndReason = sessionSamplesReached
		}
		l.issueDue(now)
		if l.outstanding == 0 && l.row.EndReason != "" && (l.terminal || l.row.SentInputs%c.batch == 0) {
			break
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		var fire <-chan time.Time
		if wake := l.wake(now); !wake.IsZero() {
			timer.Reset(wake.Sub(now))
			fire = timer.C
		}
		done := ctx.Done()
		if l.terminal {
			done = nil
		}
		select {
		case <-fire:
		case <-done:
			l.end(sessionFailed, "", ctx.Err())
		case result := <-l.results:
			l.receive(result)
		}
	}
	return l.finish(), l.failure
}

// sessionLevelRun is the loop state of one level.
type sessionLevelRun struct {
	s                 *sessionLoad
	ctx               context.Context
	cancel            context.CancelFunc
	row               sessionLevel
	request           loadRequestContext
	lagEnd, windowEnd time.Time
	counter           sessionCounter
	rng               *rand.Rand
	rate              float64
	// limit caps outstanding requests; next is the next mid arrival or sparse batch.
	limit, outstanding int
	next               time.Time
	results            chan sessionResult
	terminal           bool
	failure            error
}

func (l *sessionLevelRun) arrivalGap() time.Duration {
	return time.Duration(max(1, math.Round(l.rng.ExpFloat64()/l.rate*1e9)))
}

// predict records the expected exposure without refusing actual measurement.
func (l *sessionLevelRun) predict(interval time.Duration) {
	c := l.s.config
	predicted := max(c.meterLag+c.powerWindow, time.Duration(c.minSamples)*interval)
	l.row.PredictedSeconds, l.row.LimitSeconds = predicted.Seconds(), c.levelLimit.Seconds()
}

// issuing stops new requests only on a fixed batch boundary: native holds a
// partial batch until its lifetime expires.
func (l *sessionLevelRun) issuing() bool {
	return !l.terminal && (l.row.EndReason == "" || l.row.SentInputs%l.s.config.batch != 0)
}

func (l *sessionLevelRun) issueDue(now time.Time) {
	switch l.row.Kind {
	case sessionSaturation:
		for l.issuing() && l.outstanding < l.limit {
			l.issue(time.Now())
		}
	case sessionMid:
		for l.issuing() && l.outstanding < l.limit && !l.next.After(now) {
			l.issue(l.next)
			l.next = l.next.Add(l.arrivalGap())
		}
	case sessionSparse:
		if l.issuing() && l.outstanding == 0 && !l.next.After(now) {
			for range l.s.config.batch {
				l.issue(now)
			}
		}
	}
}

func (l *sessionLevelRun) issue(scheduled time.Time) {
	s, ctx, request := l.s, l.ctx, l.request
	id, index := l.row.Kind+"-"+strconv.Itoa(l.row.SentInputs), s.sent
	s.sent++
	l.row.SentInputs++
	l.outstanding++
	go func() { l.results <- s.send(ctx, request, id, index, scheduled) }()
}

func (l *sessionLevelRun) wake(now time.Time) time.Time {
	var wake time.Time
	for _, at := range [...]time.Time{l.windowEnd, l.next} {
		if at.After(now) && (wake.IsZero() || at.Before(wake)) {
			wake = at
		}
	}
	return wake
}

func (l *sessionLevelRun) end(reason, detail string, err error) {
	l.row.EndReason, l.row.Detail = reason, sessionBounded(detail, sessionDetailBytes)
	l.failure, l.terminal = err, true
	l.cancel()
}

func (l *sessionLevelRun) receive(result sessionResult) {
	l.outstanding--
	var nativeErr *sessionNativeError
	isNative := errors.As(result.err, &nativeErr)
	switch {
	case result.err == nil:
		l.count(result)
	case l.terminal:
		// Requests cancelled or failing after the level's end add no fact.
	case isNative && nativeErr.allocation && l.s.batches+l.counter.batches == 0:
		l.end(sessionAllocationBeforeBatch, nativeErr.message, nil)
	case isNative && nativeErr.allocation:
		l.end(sessionAllocationAfterBatch, nativeErr.message, nil)
	case isNative && l.row.Kind != sessionSaturation && nativeErr.status == http.StatusRequestTimeout:
		// Drain HTTP responses, then waitIdle observes physical completion.
		// Expired levels cannot identify execution counts and are excluded.
		l.row.EndReason, l.row.Detail = sessionRequestLifetimeExpired, sessionBounded(nativeErr.message, sessionDetailBytes)
		l.terminal = true
	case isNative && l.row.Kind == sessionSaturation && nativeErr.status == http.StatusRequestTimeout:
		// Saturation requires two batches plus delivery margin within lifetime.
		l.end(sessionServiceOverLifetime, nativeErr.message, nil)
	default:
		l.end(sessionFailed, "", result.err)
	}
}

func (l *sessionLevelRun) count(result sessionResult) {
	if !result.at.Before(l.lagEnd) {
		l.counter.afterLag++
	}
	service, completed := l.counter.add(result.progress)
	if !completed || l.terminal {
		return
	}
	c := l.s.config
	switch l.row.Kind {
	case sessionSparse:
		l.next = result.at.Add(time.Duration(l.rng.ExpFloat64() * float64(c.sparseMeanGap)))
		if l.counter.batches == 1 {
			l.predict(service + c.sparseMeanGap)
		}
	case sessionSaturation:
		if l.counter.batches != 1 {
			return
		}
		l.predict(service)
	}
}

func (l *sessionLevelRun) finish() sessionLevel {
	ended, row := time.Now(), l.row
	row.EndedAt = ended.UTC()
	row.CompletedInputs, row.CompletedBatches = l.counter.inputs, l.counter.batches
	if span := ended.Sub(l.lagEnd); span > 0 {
		row.AchievedInputsPerSecond = float64(l.counter.afterLag) / span.Seconds()
	}
	l.s.batches += l.counter.batches
	return row
}

// sessionProgress is the part of a native response the session counts.
type sessionProgress struct {
	BatchID       uint64                `json:"batchID"`
	BatchSize     int                   `json:"batchSize"`
	ServiceSample *native.ServiceSample `json:"serviceSample"`
}

// sessionCounter is a level's whole retained state: batches still awaiting
// member responses, bounded by the outstanding requests, and counts.
type sessionCounter struct {
	open                               map[uint64]int
	batches, inputs, afterLag, samples int
}

func newSessionCounter() sessionCounter {
	return sessionCounter{open: make(map[uint64]int)}
}

// add counts one member response and reports the batch's service time when
// this response completes the batch. A batch counts as a service sample once;
// a startup execution, which has no preceding idle gap, does not.
func (c *sessionCounter) add(p sessionProgress) (time.Duration, bool) {
	c.inputs++
	service := time.Duration(p.ServiceSample.ServiceDurationNanos)
	remaining, seen := c.open[p.BatchID]
	if !seen {
		remaining = p.BatchSize
		if p.ServiceSample.PrecedingIdleNanos != nil {
			c.samples++
		}
	}
	if remaining--; remaining > 0 {
		c.open[p.BatchID] = remaining
		return service, false
	}
	delete(c.open, p.BatchID)
	c.batches++
	return service, true
}

type sessionResult struct {
	progress sessionProgress
	at       time.Time
	err      error
}

// sessionNativeError is a non-200 native answer with its status and the first
// line of its body.
type sessionNativeError struct {
	status     int
	allocation bool
	message    string
}

func (e *sessionNativeError) Error() string { return e.message }

func (s *sessionLoad) send(
	ctx context.Context, requestContext loadRequestContext, id string, index int, scheduled time.Time,
) sessionResult {
	inputs, err := s.source.recordInputs(ctx, s.records[index%len(s.records)])
	if err != nil {
		return sessionResult{err: err}
	}
	response := profilingResponseBuffers.Get().(*bytes.Buffer)
	defer profilingResponseBuffers.Put(response)
	request := requestContext.nativeRequest(id, scheduled, time.Now())
	status, err := s.client.InferNative(ctx, s.inferURL, request, inputs, response)
	result := sessionResult{at: time.Now(), err: err}
	switch {
	case err != nil:
	case status != http.StatusOK:
		var failure native.ErrorResponse
		_ = json.Unmarshal(response.Bytes(), &failure)
		line, _, _ := strings.Cut(strings.TrimSpace(response.String()), "\n")
		result.err = &sessionNativeError{
			status:     status,
			allocation: failure.ExecutionComplete && failure.FailureReason == native.FailureMemoryAllocation,
			message:    fmt.Sprintf("native inference HTTP %d: %s", status, strings.TrimSpace(line)),
		}
	default:
		var body struct {
			Progress sessionProgress `json:"progress"`
		}
		if err := json.Unmarshal(response.Bytes(), &body); err != nil {
			result.err = fmt.Errorf("decode native inference response: %w", err)
		} else if p := body.Progress; p.BatchID == 0 || p.BatchSize != s.config.batch || p.ServiceSample == nil ||
			p.ServiceSample.ServiceDurationNanos <= 0 {
			result.err = fmt.Errorf("native inference response lacks the service of a completed batch of %d", s.config.batch)
		}
		result.progress = body.Progress
	}
	return result
}

// sessionTerminationMessage preserves every level row and shortens diagnostic
// text until the result fits the container termination message.
func sessionTerminationMessage(levels []sessionLevel, failure error) ([]byte, error) {
	report := struct {
		Error  string         `json:"error,omitempty"`
		Levels []sessionLevel `json:"levels"`
	}{Levels: slices.Clone(levels)}
	if failure != nil {
		report.Error = sessionBounded(failure.Error(), sessionErrorBytes)
	}
	for {
		message, err := json.Marshal(report)
		if err != nil || len(message) <= sessionTerminationBytes {
			return message, err
		}
		shortened := false
		for index := range report.Levels {
			if detail := report.Levels[index].Detail; detail != "" {
				report.Levels[index].Detail, shortened = sessionBounded(detail, len(detail)/2), true
			}
		}
		switch {
		case shortened:
		case report.Error != "":
			report.Error = sessionBounded(report.Error, len(report.Error)/2)
		default:
			return nil, errors.New("session-load level table exceeds the termination message limit")
		}
	}
}

func sessionBounded(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}
