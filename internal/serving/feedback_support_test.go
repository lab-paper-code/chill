package serving

import (
	"context"
	"testing"
)

func TestFeedbackMissingRawSupportWaitsWithoutDeletingEvidence(t *testing.T) {
	settings, snapshot, candidate := feedbackFixture()
	profile := snapshot.Instances[0].Profile
	raw := profile.Batches[0].ServiceSamples
	profile.Batches[0].ServiceDistribution = nil
	result, err := EvaluateFeedbackCandidate(context.Background(), settings, snapshot, candidate, 1)
	if err != nil || len(result.InvalidReasons) != 0 || len(result.Workloads) != 1 || result.Workloads[0].Unfinished != 2 || result.Workloads[0].OnTime+result.Workloads[0].Late != 0 || len(profile.Batches[0].ServiceSamples) != len(raw) || &profile.Batches[0].ServiceSamples[0] != &raw[0] {
		t.Fatalf("missing numerical support must retain raw evidence and all outcomes: %+v %v", result, err)
	}
}
