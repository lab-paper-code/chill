package serving

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// A real file-backed immutable boundary keeps archive tests about retained
// objects and failures, not about a mock returning the input it received.
type observationArchive struct {
	directory string
	fail      bool
	failKey   string
	mu        sync.Mutex
	outcomes  map[string]evidencestore.CreateOutcome
	// puts counts every call per key, failed ones included.
	puts map[string]int
}

func (a *observationArchive) StreamImmutable(_ context.Context, location evidencestore.ObjectLocation, produce func(io.Writer) error) (evidencestore.Receipt, error) {
	var buffer bytes.Buffer
	if err := produce(&buffer); err != nil {
		return evidencestore.Receipt{}, err
	}
	identity := evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: int64(buffer.Len())}
	a.mu.Lock()
	if a.puts == nil {
		a.puts = map[string]int{}
	}
	a.puts[identity.Key]++
	a.mu.Unlock()
	if a.fail || a.failKey != "" && strings.Contains(identity.Key, a.failKey) {
		return evidencestore.Receipt{}, errors.New("archive unavailable")
	}
	if err := identity.Validate(); err != nil {
		return evidencestore.Receipt{}, err
	}
	data, err := io.ReadAll(&buffer)
	if err != nil || int64(len(data)) != identity.Size {
		return evidencestore.Receipt{}, errors.Join(err, errors.New("incomplete archive write"))
	}
	name := filepath.Join(a.directory, identity.Key)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	outcome := evidencestore.CreateOutcomeCreated
	if existing, err := os.ReadFile(name); err == nil {
		if !bytes.Equal(existing, data) {
			return evidencestore.Receipt{}, errors.New("immutable object conflict")
		}
		outcome = evidencestore.CreateOutcomeExisting
	} else if err := os.WriteFile(name, data, 0600); err != nil {
		return evidencestore.Receipt{}, err
	}
	a.mu.Lock()
	if a.outcomes == nil {
		a.outcomes = map[string]evidencestore.CreateOutcome{}
	}
	a.outcomes[identity.Key] = outcome
	a.mu.Unlock()
	return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome}, nil
}

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
