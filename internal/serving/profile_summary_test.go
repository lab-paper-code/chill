package serving

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestHydratedServiceSummariesDoNotNeedRawRecords(t *testing.T) {
	gap := int64(0)
	samples := []firstfloor.ServiceSample{
		{ServiceDurationNanos: 10, PrecedingIdleNanos: &gap},
		{ServiceDurationNanos: 20, PrecedingIdleNanos: &gap},
		{ServiceDurationNanos: 60, PrecedingIdleNanos: &gap},
	}
	d, err := firstfloor.NewServiceDistribution(samples)
	if err != nil {
		t.Fatal(err)
	}
	// A projection can omit raw rows without losing the hydrated population.
	profile := &Profile{Batches: []BatchProfile{{Batch: 1, ServiceDistribution: d}}}
	var sum float64
	minimum := samples[0].ServiceDurationNanos
	for _, sample := range samples {
		sum += float64(sample.ServiceDurationNanos)
		minimum = min(minimum, sample.ServiceDurationNanos)
	}
	if mean, ok := profile.MeanNanos(1); !ok || mean != sum/float64(len(samples)) {
		t.Errorf("hydrated mean=%g identified=%t", mean, ok)
	}
	if got, want := capacityThroughput(profile, 1), 1e9/float64(minimum); got != want {
		t.Errorf("hydrated throughput=%g want=%g", got, want)
	}
}
