package batchsaturation

import "testing"

func TestSnapshotValidateRejectsInvalidPlannedBatchSizes(t *testing.T) {
	tests := []struct {
		name              string
		plannedBatchSizes []int
		want              string
	}{
		{
			name: "empty",
			want: "a non-empty planned batch size set is required",
		},
		{
			name:              "non-positive entry",
			plannedBatchSizes: []int{0, 1},
			want:              "planned batch sizes must be positive",
		},
		{
			name:              "descending pair",
			plannedBatchSizes: []int{2, 1},
			want:              "planned batch sizes must be strictly increasing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (Snapshot{PlannedBatchSizes: tt.plannedBatchSizes}).Validate()
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
