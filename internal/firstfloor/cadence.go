package firstfloor

import (
	"slices"
	"time"
)

// These identify the implemented execution/model semantics, not tuning knobs.
const NativeExchangeGapBoundary = "native-exchange-end-to-next-start"
const ConditionalServiceModel = "nearest-gap-uniform-physical-trial"
const CadenceDrawRule = "uniform-selected-records-with-replacement-frozen-cycle"

// CadenceContract separates the intended population from the actual trace and
// draw sequence. It is frozen before child Runs and never discovers traces at runtime.
type CadenceContract struct {
	Target     CadenceTarget     `json:"target"`
	Provenance CadenceProvenance `json:"provenance"`
}

type CadenceTarget struct {
	LoadContext      string               `json:"loadContext"`
	Boundary         string               `json:"boundary"`
	ConditionalModel string               `json:"conditionalModel"`
	Schedule         []CadenceTargetPhase `json:"schedule"`
}

// Schedule names the selected offered-load phases, in their original order.
// Collection windows and empirical element counts are provenance, not a policy
// which silently gives different phases equal selection probability.
type CadenceTargetPhase struct {
	Name           string `json:"name"`
	OfferedLoad    string `json:"offeredLoad"`
	SharingContext string `json:"sharingContext"`
}

type CadenceProvenance struct {
	Seed     int64               `json:"seed"`
	DrawRule string              `json:"drawRule"`
	Sources  []CadenceSource     `json:"sources"`
	Phases   []CadencePhase      `json:"phases"`
	Draws    []CadenceElementRef `json:"draws"`
}

type CadenceObject struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	Size  int64  `json:"size"`
}

// Donor identity belongs to provenance; only its gaps are borrowed, never S/J.
type CadenceSource struct {
	Object             CadenceObject `json:"object"`
	Boundary           string        `json:"boundary"`
	SourceRevision     string        `json:"sourceRevision"`
	LoadContext        string        `json:"loadContext"`
	Workload           string        `json:"workload"`
	NodeUID            string        `json:"nodeUID"`
	ExecutionPath      string        `json:"executionPath"`
	PowerMode          string        `json:"powerMode"`
	PhysicalInstanceID string        `json:"physicalInstanceID"`
	TrialID            string        `json:"trialID"`
	ObservedBatch      int           `json:"observedBatch"`
	WindowStart        time.Time     `json:"windowStart"`
	WindowEnd          time.Time     `json:"windowEnd"`
	Borrowed           bool          `json:"borrowed"`
}

type CadencePhase struct {
	Name     string              `json:"name"`
	Elements []CadenceGapElement `json:"elements"`
}

type CadenceGapElement struct {
	GapNanos    int64  `json:"gapNanos"`
	SourceIndex int    `json:"sourceIndex"`
	BatchID     uint64 `json:"batchID"`
}

type CadenceElementRef struct {
	Phase        string `json:"phase"`
	ElementIndex int    `json:"elementIndex"`
}

// Draw returns the actual frozen source element and monotonically increasing
// draw identity. Repeating the frozen sequence is explicit provenance, not an
// assertion of independent arrival samples.
func (c *CadenceContract) Draw(index uint64) CadenceDraw {
	ref := c.Provenance.Draws[index%uint64(len(c.Provenance.Draws))]
	for _, phase := range c.Provenance.Phases {
		if phase.Name == ref.Phase {
			return CadenceDraw{
				TargetIdleNanos: phase.Elements[ref.ElementIndex].GapNanos,
				Ref:             CadenceDrawRef{Phase: ref.Phase, ElementIndex: ref.ElementIndex, DrawIndex: index},
			}
		}
	}
	panic("validated cadence phase is absent")
}

func (c *CadenceContract) Clone() *CadenceContract {
	if c == nil {
		return nil
	}
	out := *c
	out.Target.Schedule = slices.Clone(c.Target.Schedule)
	out.Provenance.Sources = slices.Clone(c.Provenance.Sources)
	out.Provenance.Draws = slices.Clone(c.Provenance.Draws)
	out.Provenance.Phases = slices.Clone(c.Provenance.Phases)
	for index := range out.Provenance.Phases {
		out.Provenance.Phases[index].Elements = slices.Clone(c.Provenance.Phases[index].Elements)
	}
	return &out
}

func CloneCadences(input map[int]*CadenceContract) map[int]*CadenceContract {
	if input == nil {
		return nil
	}
	out := make(map[int]*CadenceContract, len(input))
	for batch, cadence := range input {
		out[batch] = cadence.Clone()
	}
	return out
}
