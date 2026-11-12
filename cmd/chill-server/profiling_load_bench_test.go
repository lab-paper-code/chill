package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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
	for _, rate := range []float64{200, 500, 1000} {
		b.Run(fmt.Sprintf("%.0f_per_second", rate), func(b *testing.B) {
			plan := profilingTestPlan(profilingBenchmarkStubProcess(b))
			plan.OperatingPoints.PhaseDurationSeconds = 10
			plan.MaxQueueRequests = 256
			plan.MinServiceSamples = 10
			plan.MaxLatencySamples = 100000
			driver := newProfilingLoadDriver(plan, profilingTestRecords(), "", io.Discard)
			defer driver.close()
			var achieved, elapsed, p50, p95, p99, maximum, encodedBytes float64
			var inputs int
			var cpuBefore, cpuAfter syscall.Rusage
			var memoryBefore, memoryAfter runtime.MemStats
			runtime.ReadMemStats(&memoryBefore)
			if err := syscall.Getrusage(syscall.RUSAGE_SELF, &cpuBefore); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for iteration := range b.N {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				phase, err := driver.runPhase(ctx, profilingrun.Phase{ID: fmt.Sprintf("benchmark-%d", iteration), Kind: "utilization", Utilization: .5, ArrivalRatePerSecond: rate})
				cancel()
				if err != nil {
					b.Fatalf("target %.0f/s completed=%d achieved=%.3f/s lag=%+v: %v", rate, phase.CompletedInputs, phase.AchievedArrivalRatePerSecond, phase.IssueLag, err)
				}
				encoded, err := json.Marshal(phase)
				if err != nil {
					b.Fatal(err)
				}
				inputs += phase.CompletedInputs
				achieved += phase.AchievedArrivalRatePerSecond
				elapsed += phase.MeasurementElapsedSeconds
				p50 += phase.IssueLag.P50
				p95 += phase.IssueLag.P95
				p99 += phase.IssueLag.P99
				maximum = max(maximum, phase.IssueLag.Max)
				encodedBytes += float64(len(encoded))
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
			b.ReportMetric(rate, "target-req/s")
			b.ReportMetric(achieved/float64(b.N), "achieved-req/s")
			b.ReportMetric(p50/float64(b.N), "lag-p50-ms")
			b.ReportMetric(p95/float64(b.N), "lag-p95-ms")
			b.ReportMetric(p99/float64(b.N), "lag-p99-ms")
			b.ReportMetric(maximum, "lag-max-ms")
			b.ReportMetric(cpu, "cpu-seconds")
			b.ReportMetric(cpu/elapsed, "cpu-cores")
			status, err := os.ReadFile("/proc/self/status")
			if err != nil {
				b.Fatal(err)
			}
			var peakRSS uint64
			for _, line := range strings.Split(string(status), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 3 && fields[0] == "VmHWM:" {
					peakRSS, err = strconv.ParseUint(fields[1], 10, 64)
					if err != nil {
						b.Fatal(err)
					}
					peakRSS *= 1024
					break
				}
			}
			if peakRSS == 0 {
				b.Fatal("missing process VmHWM RSS observation")
			}
			b.ReportMetric(float64(peakRSS), "peak-RSS-B")
			b.ReportMetric(encodedBytes/float64(inputs), "result-B/req")
			b.ReportMetric(float64(memoryAfter.TotalAlloc-memoryBefore.TotalAlloc)/float64(inputs), "alloc-B/req")
			b.ReportMetric(float64(inputs), "completed-inputs")
			if rate == 1000 && p99/float64(b.N) >= 1 {
				b.Fatalf("1000/s over 10s requires sub-ms p99; observed %.3fms, achieved %.3f/s", p99/float64(b.N), achieved/float64(b.N))
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
