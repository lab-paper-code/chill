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
