package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// BenchmarkProfilingLoadNative runs the production Poisson scheduler, gateway
// serializer and pooled transport against a separate local native-wire stub.
// CPU, allocations and peak RSS measure only the driver process, not the stub.
//
// go test ./cmd/chill-server -run '^$' -bench '^BenchmarkProfilingLoadNative$' -benchtime=1x -count=1
func BenchmarkProfilingLoadNative(b *testing.B) {
	for _, trial := range []struct {
		rate  float64
		index int
	}{{200, 1}, {500, 1}, {1000, 1}, {1000, 2}, {1000, 3}} {
		b.Run(fmt.Sprintf("%.0f_per_second/trial_%d", trial.rate, trial.index), func(b *testing.B) {
			plan := profilingTestPlan(profilingBenchmarkStubProcess(b))
			plan.OperatingPoints.PhaseDurationSeconds = 10
			plan.MaxQueueRequests = 256
			plan.MinServiceSamples = 10
			plan.MaxLatencySamples = 100000
			driver := newProfilingLoadDriver(plan, profilingTestRecords(b), "")
			defer driver.close()
			var achieved, elapsed, p50, p95, p99, maximum, encodedBytes float64
			var scheduled, sent, completed int
			var gc profilingrun.DriverGC
			var failures []error
			var cpuBefore, cpuAfter syscall.Rusage
			var memoryBefore, memoryAfter runtime.MemStats
			runtime.ReadMemStats(&memoryBefore)
			if err := syscall.Getrusage(syscall.RUSAGE_SELF, &cpuBefore); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for iteration := range b.N {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				phase, err := driver.runPhase(ctx, profilingrun.Phase{ID: fmt.Sprintf("benchmark-%.0f-trial-%d-%d", trial.rate, trial.index, iteration), Kind: "utilization", Utilization: .5, ArrivalRatePerSecond: trial.rate})
				cancel()
				b.Logf("target=%.0f/s trial=%d iteration=%d seed=%q scheduled=%d sent=%d completed=%d offering=%.9fs measurement=%.9fs achieved=%.3f/s lag=%+v GC=%+v error=%v",
					trial.rate, trial.index, iteration, phase.ArrivalSeed, phase.ScheduledInputs, phase.SentInputs, phase.CompletedInputs, phase.IssueElapsedSeconds, phase.MeasurementElapsedSeconds, phase.AchievedArrivalRatePerSecond, phase.IssueLag, phase.DriverGC, err)
				if err != nil {
					failures = append(failures, err)
				}
				var encoded profilingByteCounter
				if encodeErr := driver.writePhase(&encoded, phase); encodeErr != nil {
					failures = append(failures, encodeErr)
				}
				scheduled += phase.ScheduledInputs
				sent += phase.SentInputs
				completed += phase.CompletedInputs
				achieved += phase.AchievedArrivalRatePerSecond
				elapsed += phase.MeasurementElapsedSeconds
				if phase.IssueLag != nil {
					p50 += phase.IssueLag.P50
					p95 += phase.IssueLag.P95
					p99 += phase.IssueLag.P99
					maximum = max(maximum, phase.IssueLag.Max)
				}
				if phase.DriverGC != nil {
					gc.Cycles += phase.DriverGC.Cycles
					gc.PauseTotalMilliseconds += phase.DriverGC.PauseTotalMilliseconds
					gc.MaxPauseMilliseconds = max(gc.MaxPauseMilliseconds, phase.DriverGC.MaxPauseMilliseconds)
				}
				encodedBytes += float64(encoded)
			}
			b.StopTimer()
			if err := syscall.Getrusage(syscall.RUSAGE_SELF, &cpuAfter); err != nil {
				b.Fatal(err)
			}
			runtime.ReadMemStats(&memoryAfter)
			cpuSeconds := func(usage syscall.Rusage) float64 {
				return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
			}
			cpu := cpuSeconds(cpuAfter) - cpuSeconds(cpuBefore)
			peakRSS := float64(cpuAfter.Maxrss) * 1024
			allocated := float64(memoryAfter.TotalAlloc - memoryBefore.TotalAlloc)
			allocations := float64(memoryAfter.Mallocs - memoryBefore.Mallocs)
			b.ReportMetric(trial.rate, "target-req/s")
			b.ReportMetric(achieved/float64(b.N), "achieved-req/s")
			b.ReportMetric(p50/float64(b.N), "lag-p50-ms")
			b.ReportMetric(p95/float64(b.N), "lag-p95-ms")
			b.ReportMetric(p99/float64(b.N), "lag-p99-ms")
			b.ReportMetric(maximum, "lag-max-ms")
			b.ReportMetric(cpu, "cpu-seconds")
			if elapsed > 0 {
				b.ReportMetric(cpu/elapsed, "cpu-cores")
			}
			b.ReportMetric(float64(gc.Cycles), "GC-cycles")
			b.ReportMetric(gc.PauseTotalMilliseconds, "total-GC-pause-ms")
			b.ReportMetric(gc.MaxPauseMilliseconds, "max-GC-pause-ms")
			b.ReportMetric(peakRSS, "peak-RSS-B")
			if sent > 0 {
				b.ReportMetric(encodedBytes/float64(sent), "result-B/req")
				b.ReportMetric(allocated/float64(sent), "alloc-B/req")
				b.ReportMetric(allocations/float64(sent), "allocs/req")
			}
			b.ReportMetric(float64(scheduled), "scheduled-inputs")
			b.ReportMetric(float64(sent), "sent-inputs")
			b.ReportMetric(float64(completed), "completed-inputs")
			// Log resource observations even when a failed trial suppresses the
			// benchmark result line. Each later trial still runs independently.
			b.Logf("driver target=%.0f/s trial=%d allocated=%.0fB allocations=%.0f CPU=%.6fs peak-RSS=%.0fB scheduled=%d sent=%d completed=%d lag-p50=%.6fms lag-p95=%.6fms lag-p99=%.6fms lag-max=%.6fms GC=%+v",
				trial.rate, trial.index, allocated, allocations, cpu, peakRSS, scheduled, sent, completed, p50/float64(b.N), p95/float64(b.N), p99/float64(b.N), maximum, gc)
			for _, err := range failures {
				b.Error(err)
			}
		})
	}
}

func TestProfilingLoadNativeStubProcess(t *testing.T) {
	if os.Getenv("CHILL_TEST_PROFILE_STUB_PROCESS") != "1" {
		t.Skip("benchmark helper process")
	}
	var requests atomic.Int64
	server := profilingNativeStub(t, &requests)
	server.Start()
	defer server.Close()
	if _, err := fmt.Fprintln(os.Stdout, server.URL); err != nil {
		t.Fatal(err)
	}
	select {}
}

func profilingBenchmarkStubProcess(b testing.TB) string {
	b.Helper()
	executable, err := os.Executable()
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, executable, "-test.run=^TestProfilingLoadNativeStubProcess$", "-test.paniconexit0=false")
	command.Env = append(os.Environ(), "CHILL_TEST_PROFILE_STUB_PROCESS=1")
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		b.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		cancel()
		b.Fatal(err)
	}
	b.Cleanup(func() {
		cancel()
		_ = command.Wait()
	})
	endpoint, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || !strings.HasPrefix(endpoint, "http://127.0.0.1:") {
		b.Fatalf("native stub endpoint=%q: %v", endpoint, err)
	}
	return strings.TrimSpace(endpoint)
}
