package serving

import (
	"testing"
	"time"
)

func TestSelectConditionProfilesReplacementLineage(t *testing.T) {
	const replacementID = "replacement"
	q1 := Profile{ID: "q1", MeasuredAt: time.Unix(1, 0)}
	q2 := q1
	q2.ID, q2.MeasuredAt = "q2", time.Unix(2, 0)
	q3 := q1
	q3.ID, q3.MeasuredAt = "q3", time.Unix(3, 0)
	replacement := q1
	replacement.ID, replacement.MeasuredAt = replacementID, time.Unix(4, 0)
	replacement.Condition.ActiveProtocol.DurationSeconds = 60
	newer := q1
	newer.ID, newer.MeasuredAt = "newer", time.Unix(5, 0)
	for _, tc := range []struct {
		name     string
		profiles []Profile
		replaced map[string]bool
		want     []string
	}{
		{"replacement retires target lineage across keys", []Profile{q2, replacement, q1, q3}, map[string]bool{"q3": true}, []string{replacementID}},
		{"without replacement latest per key", []Profile{q2, replacement, q1, q3}, nil, []string{"q3", replacementID}},
		{"newer same key survives replacement", []Profile{q2, replacement, q1, q3, newer}, map[string]bool{"q3": true}, []string{replacementID, "newer"}},
		{"newer same key without replacement", []Profile{q2, q1, q3, newer}, nil, []string{"newer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eligible, err := ExcludeReplacedConditionProfiles(tc.profiles, tc.replaced)
			if err != nil {
				t.Fatal(err)
			}
			got, err := SelectConditionProfiles(eligible, nil)
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("selection=%+v error=%v, want %v", got, err, tc.want)
			}
			for i := range got {
				if got[i].ID != tc.want[i] {
					t.Fatalf("selection=%+v, want %v", got, tc.want)
				}
			}
		})
	}
}
