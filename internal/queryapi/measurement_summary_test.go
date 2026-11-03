package queryapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func restoreSummary(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		t.Fatal(err)
	}
	projection := root["readProjection"].(map[string]any)
	if projection["version"] != "measurement-summary.v1" {
		t.Fatalf("unknown projection: %v", projection["version"])
	}
	values := []any{}
	var restore func(any) any
	restore = func(value any) any {
		if text, ok := value.(string); ok && len(text) > 1 && text[0] == '~' && strings.Trim(text[1:], "0123456789") == "" {
			index, err := strconv.Atoi(text[1:])
			if err != nil || index < 0 || index >= len(values) {
				t.Fatalf("invalid or forward reference: %v", text)
			}
			return values[index]
		}
		switch item := value.(type) {
		case map[string]any:
			if parts, encoded := item["$s"].([]any); encoded && len(item) == 1 {
				text := make([]string, len(parts))
				for i, part := range parts {
					text[i] = restore(part).(string)
				}
				return strings.Join(text, "/")
			}
			if fields, encoded := item["$o"].([]any); encoded && len(item) == 1 {
				index, err := strconv.Atoi(string(fields[0].(json.Number)))
				shapes := projection["shapes"].([]any)
				if err != nil || index < 0 || index >= len(shapes) {
					t.Fatalf("invalid shape: %v", fields[0])
				}
				keys := shapes[index].([]any)
				if len(fields) != len(keys)+1 {
					t.Fatal("shape length mismatch")
				}
				object := map[string]any{}
				for i, key := range keys {
					object[key.(string)] = restore(fields[i+1])
				}
				return object
			}
			for key, entry := range item {
				item[key] = restore(entry)
			}
		case []any:
			for i := range item {
				item[i] = restore(item[i])
			}
		}
		return value
	}
	for _, entry := range projection["values"].([]any) {
		if _, literal := entry.(string); literal {
			values = append(values, entry)
		} else {
			values = append(values, restore(entry))
		}
	}
	delete(root, "readProjection")
	return restore(root).(map[string]any)
}

func assertSummaryRoundTrip(t *testing.T, raw []byte, omitPreparations bool) []byte {
	t.Helper()
	summary, err := MeasurementSummaryJSON(json.RawMessage(raw), omitPreparations)
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&original); err != nil {
		t.Fatal(err)
	}
	if omitPreparations {
		delete(original, "preparations")
	}
	if _, coverage := original["cells"]; coverage {
		delete(original, "entities")
		delete(original, "groups")
	}
	if items, list := original["items"].([]any); list {
		for _, value := range items {
			metadata := value.(map[string]any)["metadata"].(map[string]any)
			delete(metadata, "managedFields")
			delete(metadata, "annotations")
		}
	}
	if restored := restoreSummary(t, summary); !reflect.DeepEqual(original, restored) {
		t.Fatal("summary changed retained fields, check results, reasons, sources or page metadata")
	}
	return summary
}

func TestMeasurementSummarySharesEvidenceWithoutChangingChecks(t *testing.T) {
	sources := make([]string, 150)
	for i := range sources {
		sources[i] = fmt.Sprintf("evidence/profiling-runs/%03d/%s.json", i, strings.Repeat("a", 80))
	}
	checks := []any{}
	for i := range 20 {
		checks = append(checks, map[string]any{"code": fmt.Sprint(i), "result": "unknown", "message": "전력 출처 미확인", "sourceIds": sources})
	}
	rows := []any{}
	for batch := range 12 {
		rows = append(rows, map[string]any{"batch": batch + 1, "condition": map[string]any{"node": strings.Repeat("frozen-node-", 20)}, "assessment": map[string]any{"checklist": map[string]any{"checks": checks, "criteriaMet": nil}, "reasons": []any{map[string]any{"code": "Unavailable", "message": "미확인 사유", "sourceIds": sources}}}, "completedItems": json.Number("18446744073709551615")})
	}
	raw, err := json.Marshal(map[string]any{"profiles": rows, "literal": "~0", "reservedObject": map[string]any{"$o": []any{1}}, "preparations": map[string]any{"unused": strings.Repeat("x", 10000)}, "page": map[string]any{"offset": 100, "limit": 50, "total": 259, "nextOffset": 150}})
	if err != nil {
		t.Fatal(err)
	}
	summary := assertSummaryRoundTrip(t, raw, true)
	if len(summary) >= 2_000_000 || len(summary)*10 >= len(raw) {
		t.Fatalf("summary failed size ceiling: %d -> %d", len(raw), len(summary))
	}
}

// Set this to a directory of authenticated GET bodies; no credentials are read
// or network requests made by the test. Fixtures remain outside version control.
func TestMeasurementSummaryOperationalPayloads(t *testing.T) {
	dir := os.Getenv("CHILL_MEASUREMENT_FIXTURE_DIR")
	if dir == "" {
		t.Skip("operational GET fixture directory not supplied")
	}
	for _, name := range []string{"coverage", "beo", "profiles"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			summary := assertSummaryRoundTrip(t, raw, name == "profiles")
			t.Logf("%s: %d -> %d bytes", name, len(raw), len(summary))
			if name != "beo" {
				if len(summary) >= 1_000_000 {
					t.Fatalf("initial summary exceeds 1MB: %d", len(summary))
				}
				return
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatal(err)
			}
			var items []json.RawMessage
			if err := json.Unmarshal(root["items"], &items); err != nil {
				t.Fatal(err)
			}
			for offset := 0; offset < len(items); offset += 25 {
				end := min(offset+25, len(items))
				root["items"], err = json.Marshal(items[offset:end])
				if err != nil {
					t.Fatal(err)
				}
				info := map[string]int{"offset": offset, "limit": 25, "total": len(items)}
				if end < len(items) {
					info["nextOffset"] = end
				}
				root["page"], err = json.Marshal(info)
				if err != nil {
					t.Fatal(err)
				}
				page, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				summary = assertSummaryRoundTrip(t, page, false)
				t.Logf("beo page offset=%d count=%d bytes=%d", offset, end-offset, len(summary))
				if len(summary) >= 1_000_000 {
					t.Fatalf("initial BEO page exceeds 1MB: %d", len(summary))
				}
			}
		})
	}
}

func TestOccupancySummaryRetainsSignedTuplesWithinDecoderBudget(t *testing.T) {
	d := int64(-9223372036854775807)
	throughput, idle := 52.24, 0.012
	cells := make([]MeasurementCoverageCell, 390)
	for i := range cells {
		cells[i].ID = fmt.Sprintf("cell-%d", i)
		cells[i].Occupancy = []MeasurementOccupancy{}
	}
	// The retained operating snapshot has 150 DOM requests across 390 cells.
	// Even if every request has a different physical tuple, do not fan out
	// inventory nodes and profile batches into invented unmeasured entries.
	for request := range 150 {
		cell, node, batch := request%len(cells), request%3, request%6
		cells[cell].Occupancy = append(cells[cell].Occupancy, MeasurementOccupancy{
			Node:      MeasurementIdentity{Name: fmt.Sprintf("node-%d", node), UID: fmt.Sprintf("physical-node-uid-%d", node)},
			ProfileID: fmt.Sprintf("profile-%d", cell), Batch: batch + 1, State: "present",
			Request:  &MeasurementIdentity{Name: fmt.Sprintf("occ-%d", request), UID: fmt.Sprintf("request-uid-%d", request)},
			D:        &OccupancyValue{PerRequestNanos: &d, Throughput: &throughput, IdleFraction: &idle, Verdict: "Accepted"},
			Evidence: &OccupancyEvidence{Store: "evidence", Key: fmt.Sprintf("serving/dispatch-occupancy/node-%d/request-%d.json", node, request), Size: 12345},
		})
	}
	raw, err := json.Marshal(map[string]any{"cells": cells})
	if err != nil {
		t.Fatal(err)
	}
	summary := assertSummaryRoundTrip(t, raw, false)
	restored := restoreSummary(t, summary)
	var count func(any) (int, int)
	count = func(value any) (int, int) {
		nodes, depth := 1, 1
		child := func(entry any) {
			n, d := count(entry)
			nodes += n
			if d+1 > depth {
				depth = d + 1
			}
		}
		switch item := value.(type) {
		case map[string]any:
			for _, entry := range item {
				child(entry)
			}
		case []any:
			for _, entry := range item {
				child(entry)
			}
		}
		return nodes, depth
	}
	nodes, depth := count(restored)
	if nodes+230362 > 2_000_000 || depth > 64 || len(summary) > 256<<10 {
		t.Fatalf("occupancy exceeds response/decoder budget: bytes=%d nodes=%d depth=%d", len(summary), nodes, depth)
	}
	t.Logf("390 cells, 150 recorded DOM tuples: raw=%d summary=%d expanded nodes=%d depth=%d", len(raw), len(summary), nodes, depth)
}

func TestMeasurementSummarySharesPathComponents(t *testing.T) {
	paths := make([]string, 600)
	for i := range paths {
		paths[i] = fmt.Sprintf("model-%d/device-class-with-long-name/execution-path-name/power-mode-%d/request-%d", i%5, i%9, i)
	}
	raw, err := json.Marshal(map[string]any{"sources": paths})
	if err != nil {
		t.Fatal(err)
	}
	summary := assertSummaryRoundTrip(t, raw, false)
	if len(summary)*4 >= len(raw)*3 {
		t.Fatalf("path components not shared: %d -> %d", len(raw), len(summary))
	}
	assertSummaryRoundTrip(t, []byte(`{"literal":{"$s":["~0","literal"]},"slashes":"//literal/~123/"}`), false)
}
