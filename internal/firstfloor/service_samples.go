package firstfloor

// NativeExchangeGapBoundary names the observed end-to-next-start idle interval.
const NativeExchangeGapBoundary = "native-exchange-end-to-next-start"

// ServiceSample is one physical runner exchange. A nil preceding idle denotes
// an exchange without a previous end anchor, not an observed zero gap.
// Duration and idle are integer nanoseconds from the same monotonic clock.
type ServiceSample struct {
	ServiceDurationNanos int64  `json:"serviceDurationNanos"`
	PrecedingIdleNanos   *int64 `json:"precedingIdleNanos"`
	TrialID              string `json:"trialID"`
}

func (s ServiceSample) Clone() ServiceSample {
	if s.PrecedingIdleNanos != nil {
		v := *s.PrecedingIdleNanos
		s.PrecedingIdleNanos = &v
	}
	return s
}

func CloneServiceSamples(samples []ServiceSample) []ServiceSample {
	if samples == nil {
		return nil
	}
	out := make([]ServiceSample, len(samples))
	for i, s := range samples {
		out[i] = s.Clone()
	}
	return out
}
