package firstfloor

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"strings"
	"testing"
)

func TestProgressEmissionFailuresIdentifyRequestAndPhase(t *testing.T) {
	previousLogger, previousStdout := slog.Default(), os.Stdout
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
		os.Stdout = previousStdout
	})
	closed, err := os.CreateTemp(t.TempDir(), "closed-stdout")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = closed
	for _, tc := range []struct {
		name string
		p    Progress
	}{
		{"marshal native progress", Progress{QueueMS: math.NaN()}},
		{"native progress exceeds byte limit", Progress{Definition: strings.Repeat("x", MaxProgressBytes)}},
		{"write native progress", Progress{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
			tc.p.RequestID, tc.p.Phase = "request", "running"
			emitProgress(tc.p)
			for _, want := range []string{tc.name, `"requestID":"request"`, `"phase":"running"`} {
				if !strings.Contains(logged.String(), want) {
					t.Fatalf("missing %q in diagnostic: %s", want, logged.String())
				}
			}
		})
	}
}
