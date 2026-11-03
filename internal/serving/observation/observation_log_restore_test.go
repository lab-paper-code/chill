package observation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestObservationRestoreRetainsOnlyUnfinishedRows(t *testing.T) {
	const completed, unfinished = 10000, 7
	hour := observationTestHour
	rows := map[string]*serving.RequestRow{}
	log := &ObservationLog{root: t.TempDir(), started: hour, open: map[string]*serving.RequestRow{}}
	name := log.segment(observationRequestStream, hour)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	encoder := json.NewEncoder(file)
	add := func(row serving.RequestRow, at time.Time) {
		t.Helper()
		restoreRequestRow(rows, &row, at)
		if err := encoder.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	for index := range completed + unfinished {
		id := fmt.Sprint(index)
		add(serving.RequestRow{RequestID: id, Kind: serving.RequestRowAssigned, Instance: "old"}, hour)
		if index < completed {
			add(serving.RequestRow{RequestID: id, Kind: serving.RequestRowFinal, Error: strings.Repeat("x", 1024)}, hour)
		}
		// Final rows must dominate even assignments that arrive later.
		add(serving.RequestRow{RequestID: id, Kind: serving.RequestRowAssigned, Instance: "latest"}, hour)
		add(serving.RequestRow{RequestID: id, Kind: serving.RequestRowNative, Instance: "native"}, hour)
	}
	retained := 0
	for _, row := range rows {
		if row != nil {
			retained++
			if row.Instance != "latest" || !row.AssignedHour.Equal(hour) {
				t.Fatalf("wrong assignment retained: %+v", row)
			}
		}
	}
	if len(rows) != completed+unfinished || retained != unfinished {
		t.Fatalf("entries=%d retained payloads=%d; want %d and %d", len(rows), retained, completed+unfinished, unfinished)
	}
	if err := log.restore([]time.Time{hour}); err != nil {
		t.Fatal(err)
	}
	if len(log.open) != unfinished || len(log.restored) != unfinished {
		t.Fatalf("open=%d restored=%d; want %d", len(log.open), len(log.restored), unfinished)
	}
	for _, row := range log.open {
		if row.Kind != "" || row.Instance != "latest" {
			t.Fatalf("wrong reopened assignment: %+v", row)
		}
	}
}

func TestObservationTruncateTornLineTail(t *testing.T) {
	for _, data := range []string{"", "complete\n", "torn", "complete\n" + strings.Repeat("x", 10000), strings.Repeat("x", 10000) + "\npartial"} {
		name := filepath.Join(t.TempDir(), "observations.jsonl")
		if err := os.WriteFile(name, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := truncateTornLine(name); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(name)
		want := data[:strings.LastIndexByte(data, '\n')+1]
		if err != nil || string(got) != want {
			t.Fatalf("input bytes=%d: retained bytes=%d want=%d: %v", len(data), len(got), len(want), err)
		}
	}
}
