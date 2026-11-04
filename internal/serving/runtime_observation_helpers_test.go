package serving

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readRequestRows(t *testing.T, directory string) []RequestRow {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(directory, "v1alpha5", "*", "requests"+".*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []RequestRow
	for _, name := range names {
		file, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(file)
		for decoder.More() {
			var row RequestRow
			if err := decoder.Decode(&row); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return rows
}
