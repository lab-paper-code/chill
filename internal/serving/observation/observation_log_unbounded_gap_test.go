package observation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

func TestUnboundedQueryRetainsUnknownStartAfterEmptyCleanBoot(t *testing.T) {
	for _, marker := range []string{"", "{"} {
		t.Run(map[bool]string{true: "missing", false: "malformed"}[marker == ""], func(t *testing.T) {
			clean, firstStart, now := emptyCleanBootAfterCrash(t, marker)
			ctx := context.Background()
			all, err := clean.Query(ctx, "workload", time.Time{}, time.Time{}, nil, nil, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			from := firstStart.Add(-time.Second)
			window, err := clean.Query(ctx, "workload", from, now, nil, nil, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, stream := range []string{observationRequestStream, observationManagementStream} {
				unknown := unknownStartRoot(t, all.Coverage.Gaps, stream)
				finite := unknownStartRoot(t, window.Coverage.Gaps, stream)
				if !unknown.From.IsZero() || !finite.From.Equal(from) || !unknown.To.Equal(finite.To) || !unknown.To.Equal(firstStart) {
					t.Fatalf("default and finite query disagree on persistent unknown root: all=%+v finite=%+v", unknown, finite)
				}
			}
			if all.Total != 0 || window.Total != 0 || !clean.Health().Healthy {
				t.Fatal("historical uncertainty fabricated requests or failed current recording")
			}
		})
	}
}

func emptyCleanBootAfterCrash(t *testing.T, marker string) (*ObservationLog, time.Time, time.Time) {
	t.Helper()
	ctx := context.Background()
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: time.Hour,
		Writer: &observationArchive{directory: t.TempDir()}}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(config.Directory, "async-run.json"), []byte(marker), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close(ctx) })
	active, err := os.ReadFile(filepath.Join(config.Directory, "async-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Crash image: active evidence only, without Close/Flush or stream files.
	config.Directory = t.TempDir()
	if err := os.WriteFile(filepath.Join(config.Directory, "async-run.json"), active, 0600); err != nil {
		t.Fatal(err)
	}
	crashed, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := crashed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	clean, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clean.Close(ctx) })
	clock := &observationClock{at: first.Started().Add(time.Hour)}
	clean.now = clock.now
	return clean, first.Started(), clock.now()
}

func unknownStartRoot(t *testing.T, gaps []serving.ObservationGap, stream string) serving.ObservationGap {
	t.Helper()
	for _, gap := range gaps {
		if gap.Stream == stream && gap.Boot == "unknown-predecessor" {
			return gap
		}
	}
	t.Fatalf("query hid unknown-start recovery root for %s: %+v", stream, gaps)
	return serving.ObservationGap{}
}

func TestCleanPredecessorUnknownStartProvenanceInDefaultQuery(t *testing.T) {
	ctx := context.Background()
	oldTo := time.Date(2026, 11, 5, 1, 23, 28, 0, time.UTC)
	floor := oldTo.Add(2 * time.Hour)
	config := ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: time.Hour,
		Writer: &observationArchive{directory: t.TempDir()}}
	marker := observationRunState{Started: oldTo, Clean: true, Unknown: true,
		Gaps: []serving.ObservationGap{{To: oldTo, Reason: "UncleanObservationTail", Boot: "unknown-predecessor"}}}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.Directory, "async-run.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := NewObservationLog(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close(ctx) })
	clock := &observationClock{at: floor}
	log.now, log.floor = clock.now, floor
	raw, err := log.RawHour(ctx, observationHour(oldTo))
	if err != nil {
		t.Fatal(err)
	}
	persisted := unknownStartRoot(t, raw.Gaps, "")
	all, err := log.Query(ctx, "workload", time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	from := oldTo.Add(-time.Hour)
	finite, err := log.Query(ctx, "workload", from, floor, nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{observationRequestStream, observationManagementStream} {
		unknown := unknownStartRoot(t, all.Coverage.Gaps, stream)
		window := unknownStartRoot(t, finite.Coverage.Gaps, stream)
		if !unknown.From.IsZero() || !unknown.To.Equal(persisted.To) || unknown.Reason != persisted.Reason ||
			!window.From.Equal(from) || !window.To.Equal(unknown.To) || window.Boot != unknown.Boot {
			t.Fatalf("raw/default/window lost historical provenance: raw=%+v all=%+v window=%+v", persisted, unknown, window)
		}
	}
}
