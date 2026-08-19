package profilederivation

import (
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

// powerWindow is the shared scientific-quality input used by active and idle
// admission. Mode-specific completeness gates remain in their admitters.
type powerWindow struct {
	Start                    time.Time
	End                      time.Time
	Samples                  []Sample
	Failures                 int
	SourceTimestampAbsent    bool
	ConcurrentCHILLWorkloads []string
	ExternalWorkloadChanges  []string
	WorkloadWindowKnown      bool
	ThermalStateKnown        bool
	ThermallyThrottled       bool
}

type powerWindowAssessment struct {
	Reasons         []Reason
	TotalWallJoules *float64
}

func assessPowerWindow(window powerWindow, policy AdmissionPolicy) powerWindowAssessment {
	assessment := powerWindowAssessment{}
	add := func(kind Verdict, code, message string) {
		assessment.Reasons = append(assessment.Reasons, Reason{Kind: kind, Code: code, Message: message})
	}
	if window.Failures > 0 {
		add(VerdictInsufficient, "PowerReadFailures", "power observation contains failed reads")
	}
	if len(window.Samples) < policy.MinimumSamples {
		add(VerdictInsufficient, "InsufficientSamples", "power sample count is below policy")
	}
	if window.SourceTimestampAbsent && !policy.AllowReceiptTimestamps {
		add(VerdictInsufficient, "SourceTimestampUnavailable", "policy does not admit observer receipt timestamps")
	}
	if !window.ThermalStateKnown {
		add(VerdictInsufficient, "ThermalStateUnavailable", "thermal state was not captured")
	}
	if window.ThermallyThrottled {
		add(VerdictRejected, "ThermalOrRuntimeThrottling", "trial is marked throttled")
	}
	if policy.RejectConcurrentCHILLWorkloads && len(window.ConcurrentCHILLWorkloads) > 0 {
		add(VerdictRejected, "ConcurrentCHILLWorkload", "another CHILL measurement workload overlaps the Run")
	}
	if policy.RejectExternalWorkloadChanges && len(window.ExternalWorkloadChanges) > 0 {
		add(VerdictRejected, "ExternalWorkloadChanged", "external Pod membership changed during the measurement window")
	}
	if !window.WorkloadWindowKnown {
		add(VerdictInsufficient, "WorkloadWindowUnknown", "continuous measurement-window workload observation is unavailable")
	}
	joules, qualityErr := powerwindow.Integrate(powerwindow.Window{
		Start:   window.Start,
		End:     window.End,
		Samples: powerWindowSamples(window.Samples),
	}, powerwindow.Policy{
		MaximumBoundaryDistance: time.Duration(policy.MaximumBoundaryDistanceSeconds * float64(time.Second)),
		MinimumCoverage:         policy.MinimumCoverage,
		MaximumGap:              time.Duration(policy.MaximumGapSeconds * float64(time.Second)),
	})
	if qualityErr != nil {
		add(VerdictInsufficient, "PowerWindowCoverage", qualityErr.Error())
	} else {
		assessment.TotalWallJoules = &joules
	}
	return assessment
}

func powerWindowSamples(samples []Sample) []powerwindow.Sample {
	converted := make([]powerwindow.Sample, len(samples))
	for index, sample := range samples {
		converted[index] = powerwindow.Sample{
			ObservedAt: sample.ObservedAt,
			Watts:      sample.Watts,
		}
	}
	return converted
}
