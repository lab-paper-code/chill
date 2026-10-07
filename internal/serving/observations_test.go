package serving

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// A real file-backed immutable boundary keeps archive tests about retained
// objects and failures, not about a mock returning the input it received.
type observationArchive struct {
	directory string
	fail      bool
}

func (a *observationArchive) PutImmutable(_ context.Context, identity evidencestore.ObjectIdentity, source io.Reader) (evidencestore.Receipt, error) {
	if a.fail {
		return evidencestore.Receipt{}, errors.New("archive unavailable")
	}
	if err := identity.Validate(); err != nil {
		return evidencestore.Receipt{}, err
	}
	name := filepath.Join(a.directory, identity.Key)
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	outcome := evidencestore.CreateOutcomeCreated
	if errors.Is(err, os.ErrExist) {
		outcome = evidencestore.CreateOutcomeExisting
	} else if err != nil {
		return evidencestore.Receipt{}, err
	} else {
		n, copyErr := io.Copy(file, source)
		if copyErr == nil {
			copyErr = file.Sync()
		}
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n != identity.Size {
			return evidencestore.Receipt{}, errors.Join(
				copyErr, closeErr, errors.New("incomplete archive write"), os.Remove(name),
			)
		}
	}
	return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome}, nil
}

func TestJournalCohortsSurviveRestartAndRegistration(t *testing.T) {
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir()}
	config := JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive}
	journal, err := NewJournal(config)
	if err != nil {
		t.Fatal(err)
	}
	first := &Definition{Name: "same-name", UID: "old-uid", Generation: 1, SLO: SLO{Percentile: 95, TargetLatency: time.Second}}
	changed := &Definition{Name: "same-name", UID: "old-uid", Generation: 2, SLO: SLO{Percentile: 99, TargetLatency: 2 * time.Second}}
	replacement := &Definition{Name: "same-name", UID: "new-uid", Generation: 1, SLO: first.SLO}
	for _, d := range []*Definition{first, changed, replacement} {
		if err := journal.DefinitionRecord(d); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	appendEvent := func(id string, d *Definition, kind, outcome string, elapsed int64) {
		t.Helper()
		if err := journal.RequestEvent(RequestEvent{RequestID: id, WorkloadName: d.Name, WorkloadUID: d.UID, Definition: d.Identity(), Kind: kind, Outcome: outcome, At: now.Add(time.Duration(elapsed)), ReceivedAt: now, ElapsedNanos: elapsed}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("finished", first, "received", "", 0)
	appendEvent("finished", first, "accepted", "", 0)
	appendEvent("finished", first, "response_finished", "completed", 100)
	appendEvent("finished", first, "backend_finished", "error", 200)
	appendEvent("unfinished", changed, "received", "", 0)
	appendEvent("unfinished", changed, "accepted", "", 0)
	appendEvent("unfinished", changed, "backend_finished", "completed", 300)
	appendEvent("unfinished", changed, "response_finished", outcomeIncomplete, 350)
	appendEvent("delivery", first, "received", "", 0)
	appendEvent("delivery", first, "response_finished", "delivery_failed", 250)
	appendEvent("canceled", first, "received", "", 0)
	appendEvent("canceled", first, "accepted", "", 0)
	appendEvent("canceled", first, "response_finished", "canceled", 500)
	appendEvent("new", replacement, "received", "", 0)
	appendEvent("new", replacement, "response_finished", "completed", 999)
	if err := journal.ManagementEvent(ManagementEvent{WorkloadUID: first.UID, Kind: "delete", Phase: "applied", Before: changed, At: now}); err != nil {
		t.Fatal(err)
	}
	checkJournalCohortPopulation(t, journal, archive, first, changed, now)
	restored, err := NewJournal(config)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := restored.Query(first.UID, now, now.Add(time.Second), []string{changed.Identity()}, []string{outcomeIncomplete}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Total != 1 || selected.Selected != 1 || len(selected.Management) != 1 || selected.Cohorts[0].QuantileNanos != nil {
		t.Fatalf("restart/filter changed facts: %+v", selected)
	}
	newUID, err := restored.Query(replacement.UID, now, now.Add(time.Second), nil, nil, 0, "")
	if err != nil || newUID.Total != 1 {
		t.Fatalf("registration mixed UID: %+v %v", newUID, err)
	}
	empty, err := restored.Query(first.UID, now.Add(time.Second), now.Add(2*time.Second), nil, nil, 0, "")
	if err != nil || empty.Total != 0 {
		t.Fatalf("empty interval: %+v %v", empty, err)
	}
	if err := restored.Close(ctx); err != nil {
		t.Fatal(err)
	}
	config.Definitions = []string{first.Identity()}
	config.Outcomes = []string{"completed"}
	configured, err := NewJournal(config)
	if err != nil {
		t.Fatal(err)
	}
	defaultSelection, err := configured.Query(first.UID, now, now.Add(time.Second), nil, nil, 0, "")
	if err != nil || defaultSelection.Total != 3 || defaultSelection.Selected != 1 || len(defaultSelection.Cohorts) != 1 || defaultSelection.Cohorts[0].SampleCount != 1 || defaultSelection.Cohorts[0].QuantileNanos == nil || *defaultSelection.Cohorts[0].QuantileNanos != 100 {
		t.Fatalf("configured population ignored or invalid outcomes entered latency: %+v %v", defaultSelection, err)
	}
	if err := configured.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func checkJournalCohortPopulation(
	t *testing.T, journal *Journal, archive *observationArchive, first, changed *Definition, now time.Time,
) {
	t.Helper()
	before, err := journal.Query(first.UID, now, now.Add(time.Second), nil, nil, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != 4 || before.Selected != 4 ||
		before.Outcomes[outcomeIncomplete] != 1 || before.Outcomes["completed"] != 1 ||
		before.Outcomes["delivery_failed"] != 1 || before.Outcomes["canceled"] != 1 ||
		len(before.Requests) != 1 || before.NextCursor == "" || len(before.Cohorts) != 2 {
		t.Fatalf("unexpected accounting: %+v", before)
	}
	for _, c := range before.Cohorts {
		if c.Definition == changed.Identity() && (c.SampleCount != 0 || c.QuantileNanos != nil) {
			t.Fatalf("invented incomplete latency: %+v", c)
		}
		if c.Definition == first.Identity() &&
			(c.SampleCount != 1 || c.QuantileNanos == nil || *c.QuantileNanos != 100) {
			t.Fatalf("wrong response population: %+v", c)
		}
	}
	if err := journal.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := journal.Health()
	if !health.Windows[0].Published || len(health.Windows[0].Objects) != 4 {
		t.Fatalf("window not archived: %+v", health)
	}
	for _, object := range health.Windows[0].Objects {
		if _, err := os.Stat(filepath.Join(archive.directory, object.Key)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJournalArchiveFailureAndInterruptedWindow(t *testing.T) {
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir(), fail: true}
	config := JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive}
	journal, err := NewJournal(config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, kind := range []string{"received", "accepted"} {
		if err := journal.RequestEvent(RequestEvent{RequestID: "interrupted", WorkloadUID: "uid", Kind: kind, At: now, ReceivedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate process death, retaining raw streams and an open-window metadata.
	journal.mu.Lock()
	for _, file := range journal.files {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	journal.closed = true
	journal.mu.Unlock()
	oldWindow := journal.Health().Windows[0]
	file, err := os.OpenFile(filepath.Join(config.Directory, oldWindow.ID, "requests.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{\"request\":"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewJournal(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restored.Query("uid", time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil || result.Outcomes[outcomeIncomplete] != 1 || len(result.IncompleteWindows) != 1 {
		t.Fatalf("lost interrupted record: %+v %v", result, err)
	}
	if err := restored.Close(ctx); err == nil {
		t.Fatal("archive failure was hidden")
	}
	for _, w := range restored.Health().Windows {
		if w.Published {
			t.Fatal("failed archive marked published")
		}
	}
	archive.fail = false
	if err := restored.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for _, w := range restored.Health().Windows {
		if !w.Published {
			t.Fatal("archive retry did not publish")
		}
	}
}

func TestJournalArchiveFailureDoesNotPoisonRetainedRecords(t *testing.T) {
	ctx := context.Background()
	archive := &observationArchive{directory: t.TempDir(), fail: true}
	journal, err := NewJournal(JournalConfig{
		Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := journal.RequestEvent(RequestEvent{
		RequestID: "retained", WorkloadUID: "uid", Kind: "received", At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(ctx); err == nil {
		t.Fatal("archive failure was hidden")
	}
	if !journal.Health().Healthy || journal.Health().Windows[0].Published {
		t.Fatal("archive-only failure changed local durability or claimed publication")
	}
	archive.fail = false
	if err := journal.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := journal.Query("uid", time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil || result.Total != 1 || !journal.Health().Healthy || !journal.Health().Windows[0].Published {
		t.Fatalf("retained event or health lost after publication: %+v %v", result, err)
	}
}

func TestJournalAppendFailurePublishesIncompleteWindow(t *testing.T) {
	archive := &observationArchive{directory: t.TempDir()}
	journal, err := NewJournal(JournalConfig{
		Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := journal.files["requests"]
	if err := requests.Close(); err != nil {
		t.Fatal(err)
	}
	journal.files["requests"], err = os.Open(requests.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.RequestEvent(RequestEvent{
		RequestID: "lost", WorkloadUID: "uid", Kind: "received", At: time.Now().UTC(),
	}); err == nil {
		t.Fatal("readonly stream accepted an event")
	}
	if err := journal.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := journal.Health()
	if health.Healthy {
		t.Fatal("event loss did not close local durability health")
	}
	data, err := os.ReadFile(filepath.Join(archive.directory, "serving", health.Windows[0].ID, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var window ObservationWindow
	if err := json.Unmarshal(data, &window); err != nil {
		t.Fatal(err)
	}
	if !window.Incomplete {
		t.Fatal("missing event was archived as a complete window")
	}
}
