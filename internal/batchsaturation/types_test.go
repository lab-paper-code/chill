package batchsaturation

import "testing"

func TestSnapshotValidateRejectsInvalidEligibleBatches(t *testing.T) {
	tests := []struct {
		name            string
		eligibleBatches []int
		want            string
	}{
		{
			name: "empty",
			want: "a non-empty eligible batch set is required",
		},
		{
			name:            "non-positive entry",
			eligibleBatches: []int{0, 1},
			want:            "eligible batches must be positive",
		},
		{
			name:            "descending pair",
			eligibleBatches: []int{2, 1},
			want:            "eligible batches must be strictly increasing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (Snapshot{EligibleBatches: tt.eligibleBatches}).Validate()
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
