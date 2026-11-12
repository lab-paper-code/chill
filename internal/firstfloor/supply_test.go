package firstfloor

import "testing"

func TestSaturationWorkersUsesQueueBytesAndExplicitServiceBound(t *testing.T) {
	for _, test := range []struct {
		name                      string
		batch, queue              int
		bytes, request            int64
		lifetime, margin, service float64
		want                      int
	}{
		{"worker cap", 2, 100, 64 << 20, 1024, 100, 100, .1, 64},
		{"queue infeasibility is not a sizing bound", 2, 4, 64 << 20, 1024, 100, 100, .1, 0},
		{"byte cap", 2, 100, 5 * 1024, 1024, 100, 100, .1, 5},
		{"service and lifetime", 2, 100, 64 << 20, 1024, 2, 100, .2, 4},
		{"cannot fit batch", 2, 100, 1024, 1024, 1, 100, .4, 0},
		{"missing explicit service bound", 2, 100, 64 << 20, 1024, 1, 100, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := SaturationWorkers(test.batch, test.queue, test.bytes, test.request, test.lifetime, test.margin, test.service)
			if got != test.want || (err != nil) != (test.want == 0) {
				t.Fatalf("workers=%d err=%v, want %d", got, err, test.want)
			}
		})
	}
	if err := ValidateClosedLoopSupply(2, 4, 4, 64<<20, 1024, 100, 100, .1); err != nil {
		t.Fatalf("configured occupancy supply must remain feasible: %v", err)
	}
}
