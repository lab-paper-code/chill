package serving

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRequestRowAdmissionAbsentAndZero(t *testing.T) {
	var row RequestRow
	row.Fold(&RequestEvent{Policy: AdmissionPolicyDelay})
	// QueueMS includes ingress and must never supply either queue wait.
	row.FoldNative(&BackendProgress{QueueMS: 42})
	body := requestRowJSON(t, row)
	for _, name := range []string{"rejection", "gatewayWaitNanos", "executorWaitNanos"} {
		if _, exists := body[name]; exists {
			t.Fatalf("absent %s fabricated: %+v", name, body)
		}
	}
	if body["admissionPolicy"] != "Delay" {
		t.Fatalf("applied policy lost: %+v", body)
	}

	zero, remaining := int64(0), int64(-1)
	rejection := &Error{
		Code: CodeDeadlineInfeasible, Stage: AdmissionStageGateway, Policy: AdmissionPolicyReject,
		PredictedCompletionNanos: &zero, RemainingSLONanos: &remaining,
	}
	row.Fold(&RequestEvent{
		Kind: eventResponseFinished, Outcome: "rejected", Error: CodeDeadlineInfeasible,
		Policy: AdmissionPolicyReject, Rejection: rejection, GatewayWaitNanos: &zero,
	})
	row.FoldNative(&BackendProgress{ExecutorWaitNanos: &zero})
	// Fold must own pointer values; later source mutation cannot change stored facts.
	zero, remaining = 99, 99
	row.Fold(&RequestEvent{})
	row.FoldNative(&BackendProgress{QueueMS: 80})
	body = requestRowJSON(t, row)
	if body["gatewayWaitNanos"] != float64(0) || body["executorWaitNanos"] != float64(0) ||
		body["admissionPolicy"] != "Reject" || row.Outcome != "rejected" {
		t.Fatalf("zero wait or rejection facts lost: %+v", body)
	}
	decision, ok := body["rejection"].(map[string]any)
	if !ok || len(decision) != 5 || decision["code"] != CodeDeadlineInfeasible ||
		decision["stage"] != AdmissionStageGateway || decision["policy"] != "Reject" ||
		decision["predictedCompletionNanos"] != float64(0) || decision["remainingSLONanos"] != float64(-1) {
		t.Fatalf("rejection decision lost: %+v", decision)
	}
}

func TestRequestRowRejectionWithoutQueueEntry(t *testing.T) {
	var row RequestRow
	row.Fold(&RequestEvent{
		Kind: eventResponseFinished, Outcome: "rejected", Error: CodeQueueFull,
		Policy:    AdmissionPolicyDelay,
		Rejection: &Error{Code: CodeQueueFull, Stage: AdmissionStageGateway, Policy: AdmissionPolicyDelay},
	})
	body := requestRowJSON(t, row)
	decision := body["rejection"].(map[string]any)
	if len(decision) != 3 || row.Accepted() || row.GatewayWaitNanos != nil || row.ExecutorWaitNanos != nil {
		t.Fatalf("pre-accept rejection fabricated queue or estimate: %+v", body)
	}
}

func TestRequestRowWaitsUseExplicitQueueSpans(t *testing.T) {
	received := time.Unix(100, 0)
	gatewayWait, executorWait := int64(3*time.Millisecond), int64(2*time.Millisecond)
	var row RequestRow
	row.Fold(&RequestEvent{Kind: phaseAccepted, ReceivedAt: received, ElapsedNanos: int64(10 * time.Millisecond)})
	row.Fold(&RequestEvent{Kind: phaseAssigned, ElapsedNanos: int64(13 * time.Millisecond), GatewayWaitNanos: &gatewayWait})
	row.FoldNative(&BackendProgress{Phase: phaseRunning, QueueMS: 25, ExecutorWaitNanos: &executorWait})
	if row.GatewayWaitNanos == nil || *row.GatewayWaitNanos != gatewayWait ||
		row.ExecutorWaitNanos == nil || *row.ExecutorWaitNanos != executorWait {
		t.Fatalf("queue wait replaced with ingress-inclusive timing: %+v", row)
	}
}

func requestRowJSON(t *testing.T, row RequestRow) map[string]any {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestRequestRowPreservesAdmissionOutsideSupport(t *testing.T) {
	row := RequestRow{}
	row.Fold(&RequestEvent{Rejection: &Error{Code: CodeDeadlineInfeasible, Stage: AdmissionStageGateway, Policy: AdmissionPolicyReject, OutsideSupport: true}})
	body := requestRowJSON(t, row)
	decision := body["rejection"].(map[string]any)
	if decision["outsideSupport"] != true {
		t.Fatalf("support evidence lost: %+v", decision)
	}
}
