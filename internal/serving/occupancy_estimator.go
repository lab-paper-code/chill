package serving

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"gonum.org/v1/gonum/stat"
)

type OccupancyWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type OccupancyProfile struct {
	ProfileID string                         `json:"profileID"`
	Reference evidencestore.ObjectIdentity   `json:"reference"`
	Evidence  []evidencestore.ObjectIdentity `json:"evidence"`
	MeanNanos float64                        `json:"meanNanos"`
}

// OccupancyStatistics retains signed ns/request residuals. These are facts, not
// a precision, stationarity, saturation or scheduler-admission verdict.
type OccupancyStatistics struct {
	Median           float64 `json:"median"`
	P25              float64 `json:"p25"`
	P75              float64 `json:"p75"`
	IQR              float64 `json:"iqr"`
	P10              float64 `json:"p10"`
	P90              float64 `json:"p90"`
	Mean             float64 `json:"mean"`
	NegativeFraction float64 `json:"negativeFraction"`
}

type OccupancyEstimate struct {
	Node                    NodeReference        `json:"node"`
	WorkloadUID             string               `json:"workloadUID"`
	Model                   string               `json:"model"`
	PowerMode               string               `json:"powerMode"`
	ExecutionPath           string               `json:"executionPath"`
	RuntimeImage            string               `json:"runtimeImage"`
	Batch                   int                  `json:"batch"`
	Profile                 OccupancyProfile     `json:"profile"`
	Pairs                   int                  `json:"pairs"`
	Batches                 int                  `json:"batches"`
	CompleteBatches         int                  `json:"completeBatches"`
	Requests                int                  `json:"requests"`
	CompleteRequests        int                  `json:"completeRequests"`
	RawRows                 int                  `json:"rawRows"`
	CompleteRawRows         int                  `json:"completeRawRows"`
	EnrichedCompleteRawRows int                  `json:"enrichedCompleteRawRows"`
	FinalRows               int                  `json:"finalRows"`
	CompleteFinalRows       int                  `json:"completeFinalRows"`
	Statistics              *OccupancyStatistics `json:"statistics"`
	Window                  OccupancyWindow      `json:"window"`
}

type occupancyRequest struct {
	instance, profile, decision, definition, workload           string
	node                                                        NodeReference
	id                                                          uint64
	size                                                        int
	queued, running, finished                                   time.Time
	received                                                    time.Time
	finals, completeFinals, rows, completeRows                  int
	execution, nativeExecution, success, nativeSuccess, invalid bool
}

type directBatchKey struct {
	instance string
	id       uint64
}
type directCellKey struct {
	node, workload, profile string
	batch                   int
}
type directBatch struct {
	key     directBatchKey
	member  *occupancyRequest
	ready   time.Time
	members int
	valid   bool
	cell    directCellKey
}

type OccupancyEstimator struct {
	profiles map[string]*Profile
	policies map[string]firstfloor.Metadata
	window   OccupancyWindow
	requests map[string]*occupancyRequest
}

// NewOccupancyEstimator consumes the already accepted scheduler profile map;
// it does not choose a competing baseline. Policy metadata must belong to the
// physical instance, not a gateway clock or today's desired preparation.
func NewOccupancyEstimator(profiles map[string]*Profile, policies map[string]firstfloor.Metadata, from, to time.Time) *OccupancyEstimator {
	return &OccupancyEstimator{profiles: profiles, policies: policies, window: OccupancyWindow{from, to}, requests: map[string]*occupancyRequest{}}
}

func directComplete(r *occupancyRequest) bool {
	return r.id != 0 && r.size > 0 && !r.queued.IsZero() && !r.running.IsZero() && !r.finished.IsZero()
}

// retainRequest admits only the selected cohort. The scanner's private marker
// lets earlier assignment rows retain their raw denominators without admitting
// unrelated history; direct Add callers must supply an in-window anchor.
func (e *OccupancyEstimator) retainRequest(row *RequestRow) *occupancyRequest {
	if row.RequestID == "" {
		return nil
	}
	r := e.requests[row.RequestID]
	if row.Kind == RequestRowOccupancySelected {
		if r == nil {
			e.requests[row.RequestID] = &occupancyRequest{}
		}
		return nil
	}
	if r == nil && !OccupancyRowInWindow(row, e.window.Start, e.window.End) {
		return nil
	}
	if r == nil {
		r = &occupancyRequest{}
		e.requests[row.RequestID] = r
	}
	if !row.ReceivedAt.IsZero() {
		r.received = row.ReceivedAt
	}
	return r
}

// Add joins sparse late supplements before counting physical members. Repeated
// finals and conflicting identities/timestamps invalidate, rather than repair,
// the request. Assigned rows are denominators, never completed executions.
func (e *OccupancyEstimator) Add(row RequestRow) {
	r := e.retainRequest(&row)
	if r == nil {
		return
	}
	if row.Kind != RequestRowOpen {
		r.rows++
	}
	raw := occupancyRequest{id: row.BatchID, size: row.BatchSize}
	if row.NativeQueuedAt != nil {
		raw.queued = *row.NativeQueuedAt
	}
	if row.NativeRunningAt != nil {
		raw.running = *row.NativeRunningAt
	}
	if row.NativeFinishedAt != nil {
		raw.finished = *row.NativeFinishedAt
	}
	if directComplete(&raw) && row.Kind != RequestRowOpen {
		r.completeRows++
		if row.Kind == RequestRowFinal {
			r.completeFinals++
		}
	}
	merge := func(dst *string, value string) {
		if value != "" {
			if *dst != "" && *dst != value {
				r.invalid = true
			}
			*dst = value
		}
	}
	merge(&r.instance, row.Instance)
	merge(&r.profile, row.ProfileID)
	merge(&r.decision, row.DecisionID)
	merge(&r.definition, row.DefinitionID)
	merge(&r.workload, row.WorkloadUID)
	if row.Node != nil {
		merge(&r.node.Name, row.Node.Name)
		merge(&r.node.UID, row.Node.UID)
	}
	if raw.id != 0 {
		if r.id != 0 && r.id != raw.id {
			r.invalid = true
		}
		r.id = raw.id
	}
	if raw.size != 0 {
		if r.size != 0 && r.size != raw.size {
			r.invalid = true
		}
		r.size = raw.size
	}
	stamp := func(dst *time.Time, value time.Time) {
		if !value.IsZero() {
			if !dst.IsZero() && !dst.Equal(value) {
				r.invalid = true
			}
			*dst = value
		}
	}
	stamp(&r.queued, raw.queued)
	stamp(&r.running, raw.running)
	stamp(&r.finished, raw.finished)
	if row.Kind == RequestRowFinal {
		r.finals++
		r.execution = row.ExecutionComplete
		r.success = row.Outcome == outcomeCompleted && row.Error == ""
	}
	r.nativeExecution = r.nativeExecution || row.NativeExecutionComplete
	if row.NativeOutcome == outcomeCompleted {
		r.nativeSuccess = true
	}
	if row.NativeError != "" || row.NativeOutcome != "" && row.NativeOutcome != outcomeCompleted {
		r.invalid = true
	}
}

func (e *OccupancyEstimator) inside(t time.Time) bool {
	return !t.Before(e.window.Start) && t.Before(e.window.End)
}

// FirstRunningAt selects the warmup origin after native supplements have joined
// their final receipt cohort. Supplement receipt timestamps may be absent.
func (e *OccupancyEstimator) FirstRunningAt(receivedFrom, receivedTo time.Time) time.Time {
	var first time.Time
	for _, r := range e.requests {
		if r.finals == 0 || r.running.IsZero() || !ObservationInInterval(r.received, receivedFrom, receivedTo) {
			continue
		}
		if first.IsZero() || r.running.Before(first) {
			first = r.running
		}
	}
	return first
}

func (e *OccupancyEstimator) requestInWindow(r *occupancyRequest) bool {
	if !r.running.IsZero() {
		return e.inside(r.running)
	}
	return !r.received.IsZero() && e.inside(r.received)
}

func sameDirectConfiguration(a, b *occupancyRequest) bool {
	return a.node == b.node && a.profile == b.profile && a.decision == b.decision && a.definition == b.definition && a.workload == b.workload && a.size == b.size
}

// Results keeps incomplete physical batches in the sequence. No missing batch,
// restart, configuration change, policy wait, overlap or unknown timestamp can
// be bridged to manufacture a backlogged pair.
func (e *OccupancyEstimator) Results() []OccupancyEstimate {
	return e.results(false)
}

// ResultsByCell pools the actual pair samples across Workload UIDs, never
// medians of strata. Pair eligibility still requires unchanged workload/config.
func (e *OccupancyEstimator) ResultsByCell() []OccupancyEstimate {
	return e.results(true)
}

func (e *OccupancyEstimator) results(poolWorkloads bool) []OccupancyEstimate {
	cells := map[directCellKey]*OccupancyEstimate{}
	batches := e.physicalBatches(cells, poolWorkloads)
	sequence := make([]*directBatch, 0, len(batches))
	for _, b := range batches {
		b.valid = b.valid && b.members == b.member.size
		if e.inside(b.member.running) {
			cells[b.cell].Batches++
			if b.valid {
				cells[b.cell].CompleteBatches++
			}
		}
		sequence = append(sequence, b)
	}
	slices.SortFunc(sequence, func(a, b *directBatch) int {
		if c := cmp.Compare(a.key.instance, b.key.instance); c != 0 {
			return c
		}
		return cmp.Compare(a.key.id, b.key.id)
	})
	values := map[directCellKey][]float64{}
	for i := 1; i < len(sequence); i++ {
		prev, next := sequence[i-1], sequence[i]
		if e.backloggedPair(prev, next) {
			cycle := float64(next.member.running.Sub(prev.member.running).Nanoseconds())
			d := (cycle - cells[prev.cell].Profile.MeanNanos) / float64(prev.member.size)
			values[prev.cell] = append(values[prev.cell], d)
		}
	}
	out := make([]OccupancyEstimate, 0, len(cells))
	for key, cell := range cells {
		cell.Pairs = len(values[key])
		cell.Statistics = directStatistics(values[key])
		out = append(out, *cell)
	}
	slices.SortFunc(out, func(a, b OccupancyEstimate) int {
		if c := cmp.Compare(a.Node.UID, b.Node.UID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.WorkloadUID, b.WorkloadUID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Profile.ProfileID, b.Profile.ProfileID); c != 0 {
			return c
		}
		return cmp.Compare(a.Batch, b.Batch)
	})
	return out
}

func (e *OccupancyEstimator) cell(r *occupancyRequest, cells map[directCellKey]*OccupancyEstimate, poolWorkloads bool) *OccupancyEstimate {
	p := e.profiles[r.profile]
	if p == nil || r.node.UID != p.Condition.NodeUID || r.node.Name != p.NodeName {
		return nil
	}
	key := directCellKey{r.node.UID, r.workload, r.profile, r.size}
	if poolWorkloads {
		key.workload = ""
	}
	if out := cells[key]; out != nil {
		return out
	}
	mean, ok := p.MeanNanos(r.size)
	if !ok || math.IsNaN(mean) || math.IsInf(mean, 0) || mean <= 0 {
		return nil
	}
	out := &OccupancyEstimate{
		Node: r.node, WorkloadUID: key.workload, Model: p.Condition.LogicalModel,
		PowerMode: p.Condition.PowerMode, ExecutionPath: p.Condition.Artifact.ExecutionPath,
		RuntimeImage: p.Condition.RuntimeImage, Batch: r.size,
		Profile: OccupancyProfile{p.ID, p.Reference, p.Evidence, mean}, Window: e.window,
	}
	cells[key] = out
	return out
}

func (e *OccupancyEstimator) physicalBatches(cells map[directCellKey]*OccupancyEstimate, poolWorkloads bool) map[directBatchKey]*directBatch {
	batches := map[directBatchKey]*directBatch{}
	for _, r := range e.requests {
		if !e.requestInWindow(r) {
			continue
		}
		out := e.cell(r, cells, poolWorkloads)
		if out == nil {
			continue
		}
		out.Requests++
		out.RawRows += r.rows
		out.CompleteRawRows += r.completeRows
		out.FinalRows += r.finals
		out.CompleteFinalRows += r.completeFinals
		if directComplete(r) {
			out.CompleteRequests++
			out.EnrichedCompleteRawRows += r.rows
		}
		key := directBatchKey{r.instance, r.id}
		b := batches[key]
		if b == nil {
			b = &directBatch{key: key, member: r, valid: true,
				cell: directCellKey{r.node.UID, out.WorkloadUID, r.profile, r.size}}
			batches[key] = b
		}
		b.add(r)
	}
	return batches
}

func (b *directBatch) add(r *occupancyRequest) {
	valid := !r.invalid && r.finals == 1 && directComplete(r) && r.execution &&
		r.nativeExecution && r.success && r.nativeSuccess && r.instance != "" &&
		!r.queued.After(r.running) && !r.running.After(r.finished)
	b.members++
	b.valid = b.valid && valid && sameDirectConfiguration(b.member, r) &&
		b.member.running.Equal(r.running) && b.member.finished.Equal(r.finished)
	if r.queued.After(b.ready) {
		b.ready = r.queued
	}
}

func (e *OccupancyEstimator) backloggedPair(prev, next *directBatch) bool {
	a, b := prev.member, next.member
	if !prev.valid || !next.valid || prev.key.instance != next.key.instance ||
		prev.key.id == math.MaxUint64 || next.key.id != prev.key.id+1 ||
		!sameDirectConfiguration(a, b) || !e.inside(a.running) || !e.inside(b.running) {
		return false
	}
	policy, ok := e.policies[prev.key.instance]
	if !ok || policy.ProfileID != a.profile || policy.DecisionID != a.decision ||
		policy.Policy.FixedBatchSize != a.size || policy.Policy.MaxBatchWaitMS != 0 ||
		policy.AppliedAt.IsZero() || policy.AppliedAt.After(a.running) {
		return false
	}
	return !a.finished.After(b.running) && a.running.Before(b.running) && !next.ready.After(a.finished)
}

func directStatistics(sample []float64) *OccupancyStatistics {
	if len(sample) == 0 {
		return nil
	}
	slices.Sort(sample)
	q := func(p float64) float64 { return sample[int(math.RoundToEven(float64(len(sample)-1)*p))] }
	negative := 0
	for _, v := range sample {
		if v < 0 {
			negative++
		}
	}
	return &OccupancyStatistics{
		Median: q(.5), P25: q(.25), P75: q(.75), IQR: q(.75) - q(.25),
		P10: q(.1), P90: q(.9), Mean: stat.Mean(sample, nil),
		NegativeFraction: float64(negative) / float64(len(sample)),
	}
}
