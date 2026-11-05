package profilederivation

import (
	"reflect"
	"testing"
	"time"
)

func TestCurrentMeasurementsPreservesLatestFailureAndOperatorReplacement(t *testing.T) {
	old := MeasurementSelection{ID: "ns/success/uid", Model: "model", DeviceClass: "class", ExecutionPath: "path", PowerMode: "mode", MeasuredAt: time.Unix(1, 0)}
	failed := old
	failed.ID, failed.MeasuredAt = "ns/failed/uid", time.Unix(2, 0)
	tied := failed
	tied.ID = "ns/a-partial/uid"
	other := old
	other.ID, other.ExecutionPath = "ns/other/uid", "other-path"
	zero := old
	zero.ID, zero.MeasuredAt = "ns/zero/uid", time.Time{}
	candidates := []MeasurementSelection{old, failed, tied, other, zero}
	got := CurrentMeasurements(candidates, nil)
	if !reflect.DeepEqual(got, []MeasurementSelection{tied, other}) {
		t.Fatalf("latest failed/partial or tie-break lost: %+v", got)
	}
	got = CurrentMeasurements(candidates, map[string]bool{failed.ID: true, tied.ID: true})
	if !reflect.DeepEqual(got, []MeasurementSelection{other, old}) {
		t.Fatalf("operator replacement did not precede recency: %+v", got)
	}
	if got := CurrentMeasurements([]MeasurementSelection{zero}, nil); len(got) != 1 || !got[0].MeasuredAt.IsZero() {
		t.Fatal("missing time silently removed a candidate", got)
	}
	if candidates[0] != old {
		t.Fatal("selection mutated history")
	}
}

func TestMeasurementTimeUsesSuccessfulProfileThenMeasurementComplete(t *testing.T) {
	profileAt, completeAt := time.Unix(1, 0), time.Unix(2, 0)
	if got := MeasurementTime(profileAt, completeAt); !got.Equal(profileAt) {
		t.Fatal("successful profile completion replaced", got)
	}
	if got := MeasurementTime(profileAt, time.Time{}); !got.Equal(profileAt) {
		t.Fatal("missing completion discarded successful profile time", got)
	}
	if got := MeasurementTime(time.Time{}, completeAt); !got.Equal(completeAt) {
		t.Fatal("failed/partial completion time missing", got)
	}
	if got := MeasurementTime(time.Time{}, time.Time{}); !got.IsZero() {
		t.Fatal("missing completion invented a measurement time", got)
	}
}

func TestReplacedMeasurementsIgnoresMissingAndSelfTargets(t *testing.T) {
	names := map[string]string{"ns/old": "ns/old/uid", "ns/new": "ns/new/uid"}
	got := ReplacedMeasurements(map[string]string{"ns/new/uid": "ns/old", "ns/old/uid": "ns/old", "ns/missing/uid": "ns/missing"}, names)
	if !reflect.DeepEqual(got, map[string]bool{"ns/old/uid": true}) {
		t.Fatal("operator replacement did not resolve only distinct existing targets", got)
	}
}
