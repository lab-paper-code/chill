package profiler

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestServingCadenceUsesCanonicalGapRatherThanActorProxy(t *testing.T) {
	config, trace := cadenceTraceFixture()
	trace.Rows = append(trace.Rows, cadenceNativeRow(1, trace.Phases[0].Start, nil), cadenceNativeRow(2, trace.Phases[0].Start.Add(time.Millisecond), cadenceInt(73)))
	late := trace.Rows[1]
	late.Kind, late.PrecedingIdleNanos = cadenceNativeRequestKind, cadenceInt(9000)
	wrongRunning, wrongFinished := late.NativeRunningAt.Add(time.Second), late.NativeFinishedAt.Add(time.Second)
	late.NativeRunningAt, late.NativeFinishedAt = &wrongRunning, &wrongFinished
	trace.Rows = append(trace.Rows, late) // never overwrite the authoritative final
	// The actor timestamps imply 999000ns, but only the runner's actual IPC
	// boundary can report the 73ns gap. The consumer must receive the latter.
	contract, err := selectedServingCadence(config, trace)
	if err != nil {
		t.Fatal(err)
	}
	zero, native, proxy := int64(0), int64(73), int64(999000)
	consumer, err := firstfloor.NewServiceDistribution([]firstfloor.ServiceSample{
		{ServiceDurationNanos: 1000, PrecedingIdleNanos: &zero, TrialID: "target-trial"},
		{ServiceDurationNanos: 7000000, PrecedingIdleNanos: &native, TrialID: "target-trial"},
		{ServiceDurationNanos: 30000000, PrecedingIdleNanos: &proxy, TrialID: "target-trial"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range contract.Provenance.Draws {
		draw := contract.Draw(uint64(index))
		service, _, ok := consumer.QuantileNanos(&draw.TargetIdleNanos, 95)
		if !ok || service != 7000000 {
			t.Fatalf("canonical-source pacing selected zero-gap/actor-proxy service: %dns", service)
		}
	}
}

func TestServingCadenceFinalIdentityAndSparseNativeJoin(t *testing.T) {
	config, trace := cadenceTraceFixture()
	for batch := uint64(1); batch <= 2; batch++ {
		complete := cadenceNativeRow(batch, trace.Phases[0].Start.Add(time.Duration(batch)*time.Millisecond), cadenceInt(91))
		assigned := complete
		assigned.Kind, assigned.Instance, assigned.ProfileID = cadenceAssignedRequestKind, "obsolete-assignment", "obsolete-profile"
		final := complete
		final.NativeQueuedAt, final.NativeRunningAt, final.NativeFinishedAt = nil, nil, nil
		final.NativeExecutionComplete, final.NativeOutcome = nil, ""
		final.ServiceDurationNanos, final.PrecedingIdleNanos, final.NativeTrialID = 0, nil, ""
		queued, running, finished := complete, complete, complete
		queued.Kind, running.Kind, finished.Kind = cadenceNativeRequestKind, cadenceNativeRequestKind, cadenceNativeRequestKind
		queued.ProfileID, running.ProfileID, finished.ProfileID = "", "", ""
		queued.NativeRunningAt, queued.NativeFinishedAt, queued.NativeExecutionComplete, queued.NativeOutcome = nil, nil, nil, ""
		queued.ServiceDurationNanos, queued.PrecedingIdleNanos = 0, nil
		running.NativeQueuedAt, running.NativeFinishedAt, running.NativeExecutionComplete, running.NativeOutcome = nil, nil, nil, ""
		running.ServiceDurationNanos, running.PrecedingIdleNanos = 0, nil
		finished.NativeQueuedAt, finished.NativeRunningAt = nil, nil
		trace.Rows = append(trace.Rows, assigned, final, queued, running, finished)
	}
	contract, err := selectedServingCadence(config, trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := contract.Draw(0).TargetIdleNanos; got != 91 {
		t.Fatalf("final/native physical join produced %dns", got)
	}
}

func TestServingCadenceKnownPartialBatchNeverBridges(t *testing.T) {
	config, trace := cadenceTraceFixture()
	for batch := uint64(6); batch <= 8; batch++ {
		row := cadenceNativeRow(batch, trace.Phases[0].Start.Add(time.Duration(batch)*time.Millisecond), cadenceInt(37))
		if batch == 7 {
			// A known batch with sparse native progress and unresolved profile is
			// still a physical sequence barrier, not a row to filter out first.
			row.NativeFinishedAt, row.NativeExecutionComplete, row.NativeOutcome = nil, nil, ""
			row.ProfileID, row.ServiceDurationNanos, row.PrecedingIdleNanos = "", 0, nil
		}
		trace.Rows = append(trace.Rows, row)
	}
	_, err := selectedServingCadence(config, trace)
	var prerequisite *BootstrapServingTraceError
	if !errors.As(err, &prerequisite) {
		t.Fatalf("missing Nano batch 7 manufactured a cadence: %v", err)
	}
}

func TestServingCadenceNanoSevenMissingNativeRowsRemainBarriers(t *testing.T) {
	config, trace := cadenceTraceFixture()
	trace.Rows = append(trace.Rows, cadenceNativeRow(1, trace.Phases[0].Start.Add(time.Millisecond), nil), cadenceNativeRow(2, trace.Phases[0].Start.Add(2*time.Millisecond), cadenceInt(37)))
	for missing := range 7 {
		row := cadenceNativeRow(uint64(missing+10), trace.Phases[0].Start.Add(1500*time.Microsecond), nil)
		row.NativeQueuedAt, row.NativeRunningAt, row.NativeFinishedAt = nil, nil, nil
		row.NativeExecutionComplete, row.NativeOutcome = nil, ""
		row.BatchID, row.ServiceDurationNanos = 0, 0
		trace.Rows = append(trace.Rows, row)
	}
	_, err := selectedServingCadence(config, trace)
	var prerequisite *BootstrapServingTraceError
	if !errors.As(err, &prerequisite) {
		t.Fatalf("seven unplaceable native rows were dropped before adjacency: %v", err)
	}
}

func TestServingCadenceRequiresAllPhysicalBatchMembers(t *testing.T) {
	config, trace := cadenceTraceFixture()
	for batch := uint64(1); batch <= 2; batch++ {
		row := cadenceNativeRow(batch, trace.Phases[0].Start.Add(time.Duration(batch)*time.Millisecond), cadenceInt(37))
		row.BatchSize = 2 // only one member is retained
		trace.Rows = append(trace.Rows, row)
	}
	if _, err := selectedServingCadence(config, trace); err == nil {
		t.Fatal("partial physical batches became gap samples")
	}
}

func TestServingCadenceDrawsWeightActualRecordsNotPhases(t *testing.T) {
	config, trace := cadenceTraceFixture()
	config.Measurement.MinServiceSamples = 999
	start := trace.Phases[0].End
	trace.Phases = append(trace.Phases, CadenceTracePhase{
		Target: firstfloor.CadenceTargetPhase{Name: "shared-high", OfferedLoad: "40req/s", SharingContext: "recorded-shared"},
		Start:  start, End: start.Add(time.Second),
	})
	for batch := uint64(1); batch <= 2; batch++ {
		trace.Rows = append(trace.Rows, cadenceNativeRow(batch, trace.Phases[0].Start.Add(time.Duration(batch)*time.Millisecond), cadenceInt(11)))
	}
	for batch := uint64(3); batch <= 12; batch++ {
		trace.Rows = append(trace.Rows, cadenceNativeRow(batch, start.Add(time.Duration(batch)*time.Millisecond), cadenceInt(99)))
	}
	contract, err := selectedServingCadence(config, trace)
	if err != nil {
		t.Fatal(err)
	}
	low, high := 0, 0
	for index := range contract.Provenance.Draws {
		switch contract.Draw(uint64(index)).TargetIdleNanos {
		case 11:
			low++
		case 99:
			high++
		default:
			t.Fatal("a phase boundary became an invented gap")
		}
	}
	if low == 0 || high < low*3 {
		t.Fatalf("record-cardinality pacing was replaced by equal phase mixing: low=%d high=%d", low, high)
	}
	if contract.Draw(uint64(len(contract.Provenance.Draws))).TargetIdleNanos != contract.Draw(0).TargetIdleNanos {
		t.Fatal("long elapsed-floor execution lost its frozen cycle")
	}
}

func TestServingCadencePrefersMatchingCellBeforeBorrowingG(t *testing.T) {
	config, trace := cadenceTraceFixture()
	donor := trace.Profiles["profile"]
	donor.ID, donor.Condition.NodeUID = "donor", "foreign-node"
	trace.Profiles[donor.ID] = donor
	for batch := uint64(1); batch <= 2; batch++ {
		matched := cadenceNativeRow(batch, trace.Phases[0].Start.Add(time.Duration(batch)*time.Millisecond), cadenceInt(17))
		borrowed := matched
		borrowed.Instance, borrowed.ProfileID, borrowed.RequestID = "donor-pod/container", donor.ID, fmt.Sprintf("donor-%d", batch)
		borrowed.PrecedingIdleNanos = cadenceInt(900)
		trace.Rows = append(trace.Rows, matched, borrowed)
	}
	contract, err := selectedServingCadence(config, trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := contract.Draw(0).TargetIdleNanos; got != 17 {
		t.Fatalf("borrowed over matching cell: %dns", got)
	}
	// Remove all matching-cell rows; its assigned membership must not leak
	// into the donor's separate physical instance sequence.
	var donorRows []CadenceRequestRow
	for _, row := range trace.Rows {
		if row.ProfileID == donor.ID {
			donorRows = append(donorRows, row)
		}
	}
	trace.Rows = donorRows
	contract, err = selectedServingCadence(config, trace)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Draw(0).TargetIdleNanos != 900 || !contract.Provenance.Sources[contract.Provenance.Phases[0].Elements[0].SourceIndex].Borrowed {
		t.Fatal("actual donor G was fabricated or presented as matching-condition evidence")
	}
}

func TestServingCadenceFreezesEachPlannedBatchBeforeChildren(t *testing.T) {
	config, trace := cadenceTraceFixture()
	config.Measurement.WarmupBatchSizes = []int{1, 2, 4}
	for batch := uint64(1); batch <= 2; batch++ {
		trace.Rows = append(trace.Rows, cadenceNativeRow(batch, trace.Phases[0].Start.Add(time.Duration(batch)*time.Millisecond), cadenceInt(17)))
		for member := range 2 {
			row := cadenceNativeRow(batch, trace.Phases[0].Start.Add(time.Duration(batch)*time.Millisecond), cadenceInt(900))
			row.RequestID, row.Instance, row.BatchSize = fmt.Sprintf("k2-%d-%d", batch, member), "k2-pod/container", 2
			trace.Rows = append(trace.Rows, row)
		}
	}
	contracts, err := BuildServingCadences(config, trace)
	if err != nil {
		t.Fatal(err)
	}
	zero, small, large := int64(0), int64(17), int64(900)
	consumer, err := firstfloor.NewServiceDistribution([]firstfloor.ServiceSample{
		{ServiceDurationNanos: 1000, PrecedingIdleNanos: &zero, TrialID: "target-trial"},
		{ServiceDurationNanos: 7000000, PrecedingIdleNanos: &small, TrialID: "target-trial"},
		{ServiceDurationNanos: 30000000, PrecedingIdleNanos: &large, TrialID: "target-trial"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for batch, expectedService := range map[int]int64{1: 7000000, 2: 30000000} {
		contract := contracts[batch]
		if contract == nil {
			t.Fatalf("planned batch %d has no frozen source selection", batch)
		}
		for index := range contract.Provenance.Draws {
			draw := contract.Draw(uint64(index))
			service, _, ok := consumer.QuantileNanos(&draw.TargetIdleNanos, 95)
			if !ok || service != expectedService {
				t.Fatalf("batch %d reused another K's gap law: service=%dns", batch, service)
			}
		}
		for _, source := range contract.Provenance.Sources {
			if source.Borrowed {
				t.Fatalf("batch %d ignored its actual matching-condition trace", batch)
			}
		}
	}
	if contracts[4] == nil {
		t.Fatal("planned batch without local K lost its actual G-only donor law")
	}
	for _, source := range contracts[4].Provenance.Sources {
		if !source.Borrowed {
			t.Fatal("foreign observed K was represented as local K4 evidence")
		}
	}
}

func TestServingCadenceAlternatingCompleteBatchSizes(t *testing.T) {
	config, trace := cadenceTraceFixture()
	config.Measurement.WarmupBatchSizes = []int{1, 2, 4}
	for index, size := range []int{1, 2, 1, 2} {
		batch := uint64(index + 1)
		var gap *int64
		if index > 0 {
			gap = cadenceInt(int64(size * 11))
		}
		for member := range size {
			row := cadenceNativeRow(batch, trace.Phases[0].Start.Add(time.Duration(index+1)*time.Millisecond), gap)
			row.RequestID = fmt.Sprintf("batch-%d-member-%d", batch, member)
			row.BatchSize = size
			trace.Rows = append(trace.Rows, row)
		}
	}
	if len(trace.Rows) != 6 {
		t.Fatalf("alternating complete exchanges retained %d members, want 6", len(trace.Rows))
	}
	contracts, err := BuildServingCadences(config, trace)
	if err != nil {
		t.Fatal(err)
	}
	for k, wantIDs := range map[int][]uint64{1: {3}, 2: {2, 4}, 4: {2, 3, 4}} {
		contract := contracts[k]
		if contract == nil || len(contract.Provenance.Draws) == 0 || len(contract.Provenance.Phases) != 1 {
			t.Fatalf("K%d has no nonempty frozen law: %+v", k, contract)
		}
		elements := contract.Provenance.Phases[0].Elements
		if len(elements) != len(wantIDs) {
			t.Fatalf("K%d retained %d gaps, want batches %v", k, len(elements), wantIDs)
		}
		for index, id := range wantIDs {
			element := elements[index]
			observedK := 2
			if id == 3 {
				observedK = 1
			}
			source := contract.Provenance.Sources[element.SourceIndex]
			if element.BatchID != id || element.GapNanos != int64(observedK*11) ||
				source.ObservedBatch != observedK || source.Borrowed != (k == 4) {
				t.Fatalf("K%d gap %d lost actual membership or donor label: element=%+v source=%+v", k, index, element, source)
			}
		}
		for index := range contract.Provenance.Draws {
			got := contract.Draw(uint64(index)).TargetIdleNanos
			if k != 4 && got != int64(k*11) || k == 4 && got != 11 && got != 22 {
				t.Fatalf("K%d drew %dns outside its selected actual gap law", k, got)
			}
		}
	}
	// Retain K1 -> incomplete K2 -> K1. Neither adjacent gap may survive,
	// and the complete K1 exchanges must not be bridged across batch 2.
	trace.Rows = trace.Rows[:4]
	trace.Rows = append(trace.Rows[:2], trace.Rows[3:]...)
	if _, err := BuildServingCadences(config, trace); !errors.Is(err, ErrBootstrapServingTraceRequired) {
		t.Fatalf("missing member in mixed-K sequence manufactured a gap: %v", err)
	}
}

func cadenceTraceFixture() (profilingrun.RunConfig, CadenceTrace) {
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	config := profilingrun.RunConfig{TargetNode: profilingrun.NodeSnapshot{UID: "node"}, Candidate: profilingrun.CandidateIdentity{ExecutionPath: "cpu"}, State: profilingrun.ExecutionState{PowerMode: "fixed"}, Measurement: profilingrun.MeasurementContract{WorkloadUID: "workload", Batch: 1, MinServiceSamples: 59}}
	profile := CadenceTraceProfile{ID: "profile", Condition: profilederivation.CurvePlanSpec{NodeUID: "node", PowerMode: "fixed", Artifact: profilederivation.CurveArtifactContract{ExecutionPath: "cpu"}}}
	return config, CadenceTrace{LoadContext: "recorded-population", TrialID: "evaluation/stage", SourceRevision: "producer-revision", Profiles: map[string]CadenceTraceProfile{"profile": profile}, Phases: []CadenceTracePhase{{Target: firstfloor.CadenceTargetPhase{Name: "low", OfferedLoad: "2req/s", SharingContext: "recorded-alone"}, Start: start, End: start.Add(time.Second)}}}
}

func cadenceNativeRow(batch uint64, at time.Time, gap *int64) CadenceRequestRow {
	queued, finished, complete := at.Add(-time.Microsecond), at.Add(time.Microsecond), true
	return CadenceRequestRow{Kind: cadenceFinalRequestKind, RequestID: fmt.Sprintf("request-%d", batch), WorkloadUID: "workload", DefinitionID: "definition", Instance: "pod/container", ProfileID: "profile", BatchID: batch, BatchSize: 1, ReceivedAt: queued, NativeQueuedAt: &queued, NativeRunningAt: &at, NativeFinishedAt: &finished, NativeExecutionComplete: &complete, NativeOutcome: "completed", ServiceDurationNanos: 100, PrecedingIdleNanos: gap, NativeTrialID: "native-trial", Object: firstfloor.CadenceObject{Store: "evaluation", Key: "actual-requests.jsonl.gz", Size: 123}}
}

func cadenceInt(value int64) *int64 { return &value }

func selectedServingCadence(config profilingrun.RunConfig, trace CadenceTrace) (*firstfloor.CadenceContract, error) {
	contracts, err := BuildServingCadences(config, trace)
	if err != nil {
		return nil, err
	}
	return contracts[config.Measurement.Batch], nil
}
