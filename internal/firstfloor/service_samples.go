package firstfloor

// ServiceSample is one physical runner exchange. A nil preceding idle denotes
// an exchange without a previous end anchor, not an observed zero gap.
// Duration and idle are integer nanoseconds from the same monotonic clock.
type ServiceSample struct {
	ServiceDurationNanos int64           `json:"serviceDurationNanos"`
	PrecedingIdleNanos   *int64          `json:"precedingIdleNanos"`
	TargetIdleNanos      *int64          `json:"targetIdleNanos,omitempty"`
	TrialID              string          `json:"trialID"`
	TraceDrawRef         *CadenceDrawRef `json:"traceDrawRef,omitempty"`
}

// CadenceDrawRef identifies an actual draw from the frozen phase elements.
// DrawIndex continues across repetitions of the frozen draw sequence.
type CadenceDrawRef struct {
	Phase        string `json:"phase"`
	ElementIndex int    `json:"elementIndex"`
	DrawIndex    uint64 `json:"drawIndex"`
}

// CadenceDraw is a profiling overlay. Pacing is outside the exchange stopwatch
// and execution timeout; elapsed natural idle already counts toward the target.
type CadenceDraw struct {
	TargetIdleNanos int64          `json:"targetIdleNanos"`
	Ref             CadenceDrawRef `json:"ref"`
}

func (s ServiceSample) Clone() ServiceSample {
	if s.PrecedingIdleNanos != nil {
		v := *s.PrecedingIdleNanos
		s.PrecedingIdleNanos = &v
	}
	if s.TargetIdleNanos != nil {
		v := *s.TargetIdleNanos
		s.TargetIdleNanos = &v
	}
	if s.TraceDrawRef != nil {
		v := *s.TraceDrawRef
		s.TraceDrawRef = &v
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
