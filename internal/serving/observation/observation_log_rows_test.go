package observation

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// mergeRequestRowTwoPass is the former row reader: an envelope pass, then a
// decode of kept rows. mergeRequestRow must match it on every input.
func mergeRequestRowTwoPass(rows map[string]*serving.RequestRow, raw []byte, keep func(*requestEnvelope) bool) error {
	var envelope requestEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if !keep(&envelope) || envelope.Kind != serving.RequestRowFinal && rows[envelope.RequestID] != nil {
		return nil
	}
	row := &serving.RequestRow{}
	if err := json.Unmarshal(raw, row); err != nil {
		return err
	}
	rows[row.RequestID] = row
	return nil
}

var requestRowSelectors = map[string]func(*requestEnvelope) bool{
	"all": func(*requestEnvelope) bool { return true },
	"workload": func(row *requestEnvelope) bool {
		return row.WorkloadUID == "w1" && serving.ObservationInInterval(row.ReceivedAt, time.Unix(100, 0), time.Unix(200, 0))
	},
}

func requestRowCorpus() [][]byte {
	elapsed := int64(7)
	at := time.Unix(150, 5).UTC()
	rows := [][]byte{}
	for _, row := range []serving.RequestRow{
		{Kind: serving.RequestRowAssigned, RequestID: "r1", WorkloadUID: "w1", ReceivedAt: at, Node: &serving.NodeReference{}},
		{Kind: serving.RequestRowFinal, RequestID: "r1", WorkloadUID: "w1", ReceivedAt: at, ElapsedNanos: &elapsed, ServiceMS: 1.5},
		{Kind: serving.RequestRowAssigned, RequestID: "r2", WorkloadUID: "w2", ReceivedAt: at},
		{Kind: serving.RequestRowAssigned, RequestID: "r3", WorkloadUID: "w1", ReceivedAt: time.Unix(300, 0).UTC()},
	} {
		raw, _ := json.Marshal(row)
		rows = append(rows, raw)
	}
	for _, raw := range []string{
		`{"kind":5,"requestID":"r4","workloadUID":"w1","receivedAt":"1970-01-01T00:02:30Z"}`,
		`{"kind":"assigned","requestID":"r5","workloadUID":"w2","receivedAt":"1970-01-01T00:02:30Z","batchID":-1}`,
		`{"kind":"assigned","requestID":"r6","workloadUID":"w1","receivedAt":"1970-01-01T00:02:30Z","batchID":-1}`,
		`{"kind":"assigned","requestID":"r7","workloadUID":"w2","queueMS":1e999}`,
		`{"kind":"assigned","requestID":"r8","workloadUID":"w2","queueMS":01}`,
		`{"kind":"assigned","requestID":"r9","receivedAt":"x"}`,
		`{"kind":"final","requestID":"r1","requestID":"r10","workloadUID":"w1","receivedAt":"1970-01-01T00:02:30Z"}`,
		`{"KIND":"final","RequestId":"r11","workloadUID":"w1","receivedAt":"1970-01-01T00:02:30Z"}`,
		`{"kind":"final","requestID":"r12","workloadUID":"w1","receivedAt":"1970-01-01T00:02:30Z","node":[]}`,
		`{"kind":"final","requestID":"\ud800","workloadUID":"w1","receivedAt":"1970-01-01T00:02:30Z"}`,
		`{"":1e700}`,
		`{"kind":"assigned","requestID":"r13"}x`, `[]`, `null`, `{`, `{"kind":"assigned",}`, "{\"kind\":\"a\xff\"}",
	} {
		rows = append(rows, []byte(raw))
	}
	return rows
}

func TestMergeRequestRowMatchesTwoPass(t *testing.T) {
	corpus := requestRowCorpus()
	for i, raw := range corpus {
		for name, keep := range requestRowSelectors {
			want, got := map[string]*serving.RequestRow{}, map[string]*serving.RequestRow{}
			for _, row := range corpus[:i] {
				_ = mergeRequestRowTwoPass(want, row, keep)
				_ = mergeRequestRow(got, row, keep, nil)
			}
			wantErr := mergeRequestRowTwoPass(want, raw, keep)
			gotErr := mergeRequestRow(got, raw, keep, nil)
			if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || !reflect.DeepEqual(want, got) {
				t.Fatalf("%s %q: error %v want %v, rows %+v want %+v", name, raw, gotErr, wantErr, got, want)
			}
		}
	}
}

func TestRequestRowTransientStateNeverChangesJSON(t *testing.T) {
	row := serving.RequestRow{RequestID: "request", ReceivedAt: observationTestHour}
	row.FoldNative(&serving.BackendProgress{Phase: "finished"})
	before, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	row.ProfileBody = &serving.Profile{ID: "profile"}
	row.AssignedHour, row.Closing = observationTestHour, true
	after, err := json.Marshal(row)
	if err != nil || string(before) != string(after) {
		t.Fatalf("transient storage state changed the request schema: before=%s after=%s err=%v", before, after, err)
	}
}
