// Package fixtures holds captured-shape inputs shared by contract consumers.
package fixtures

import "github.com/lab-paper-code/chill/internal/firstfloor"

func ServiceSample(trialID string, callIndex uint64, serviceNanos int64) firstfloor.ServiceSample {
	sample := firstfloor.ServiceSample{ServiceDurationNanos: serviceNanos, TrialID: trialID}
	if callIndex > 0 {
		actual := int64(1_001_000)
		if callIndex%2 == 0 {
			actual = 100_001_000
		}
		sample.PrecedingIdleNanos = &actual
	}
	return sample
}

// ServiceSamples is one constant service law identified at every SLO
// percentile the tests use: the gapless first execution (dropped at
// hydration) and 459 known-gap calls, the 99th percentile's sample floor.
func ServiceSamples(trialID string, serviceNanos int64) []firstfloor.ServiceSample {
	samples := make([]firstfloor.ServiceSample, 460)
	for i := range samples {
		samples[i] = ServiceSample(trialID, uint64(i), serviceNanos)
	}
	return samples
}
