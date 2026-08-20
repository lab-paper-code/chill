package v1alpha1_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestNodeActuationObservationPreservesMicrosecondsAcrossJSON(t *testing.T) {
	t.Parallel()
	observedAt := metav1.NewMicroTime(time.Date(2026, 8, 20, 12, 34, 56, 123456000, time.UTC))
	original := edgev1alpha1.NodeActuationObservation{ObservedAt: observedAt}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal NodeActuationObservation: %v", err)
	}
	if !strings.Contains(string(encoded), `"observedAt":"2026-08-20T12:34:56.123456Z"`) {
		t.Fatalf("encoded observation = %s", encoded)
	}

	var decoded edgev1alpha1.NodeActuationObservation
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal NodeActuationObservation: %v", err)
	}
	if !decoded.ObservedAt.Equal(&observedAt) {
		t.Fatalf("decoded observedAt = %s, want %s", decoded.ObservedAt.Time, observedAt.Time)
	}
}
