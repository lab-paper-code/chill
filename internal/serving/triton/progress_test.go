package triton

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeProgressRecord(t *testing.T) {
	const record = `CHILL_PROGRESS {"requestID":"request~uid/2/model-uid/3","workloadUID":"uid","definition":"uid/2/model-uid/3","phase":"finished","at":"2026-10-06T10:20:30.123456Z"}`
	for _, line := range []string{record, "2026-10-06T10:20:31Z " + record + "\n"} {
		progress, err := ParseProgress([]byte(line))
		if err != nil || progress.RequestID != "request" || progress.WorkloadUID != "uid" || progress.Definition != "uid/2/model-uid/3" || progress.Phase != "finished" || !progress.At.Equal(time.Date(2026, 10, 6, 10, 20, 30, 123456000, time.UTC)) {
			t.Fatalf("actual native record changed: %#v %v", progress, err)
		}
	}
	for _, line := range []string{
		strings.Replace(record, `"requestID":"request~uid/2/model-uid/3"`, `"requestID":"request~uid/2/model-uid/3","requestID":"other"`, 1),
		strings.Replace(record, `"phase":"finished"`, `"phase":"responded"`, 1),
		strings.Replace(record, `"definition":"uid/2/model-uid/3"`, `"definition":"other/2/model-uid/3"`, 1),
		strings.Replace(record, `"requestID":"request~uid/2/model-uid/3"`, `"requestID":"token\nvalue"`, 1),
		strings.Replace(record, `"phase":"finished"`, `"phase":"finished","outcome":"success"`, 1),
		strings.Replace(record, `"at":"2026-10-06T10:20:30.123456Z"`, `"queueStartNanos":"100"`, 1),
		record + `{}`,
		record + strings.Repeat(" ", 4096),
	} {
		if _, err := ParseProgress([]byte(line)); err == nil {
			t.Fatal("malformed/unsafe native record accepted")
		}
	}
	for _, line := range []string{"ordinary Triton server log", "arbitrary prefix " + record} {
		if _, err := ParseProgress([]byte(line)); !errors.Is(err, ErrNotProgress) {
			t.Fatalf("unrelated log was treated as native progress: %v", err)
		}
	}
	trace := strings.Replace(record, `"phase":"finished"`, `"phase":"queue_trace","queueStartNanos":"100","computeStartNanos":"140"`, 1)
	progress, err := ParseProgress([]byte(trace))
	if err != nil || progress.RequestID != "request" || progress.NativeQueue == nil || progress.NativeQueue.QueueStartNanos != 100 || progress.NativeQueue.ComputeStartNanos != 140 {
		t.Fatalf("native queue timing lost: %+v %v", progress, err)
	}
	if _, err = ParseProgress([]byte(strings.Replace(trace, `"computeStartNanos":"140"`, `"computeStartNanos":"99"`, 1))); err == nil {
		t.Fatal("backwards native queue timing accepted")
	}
}
