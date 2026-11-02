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
			if len(summary) >= 2_000_000 {
				t.Fatalf("summary exceeds 2MB: %d", len(summary))
			}
		})
	}
}
