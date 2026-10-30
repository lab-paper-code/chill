package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// SCH-AC-53: discover real kubelet directories even with a private cgroup namespace.
func TestResourceCgroupVersionsAndMissingPeak(t *testing.T) {
	target := serving.ResourceTarget{PodUID: "aa-bb", ContainerID: "containerd://deadbeef"}
	for _, driver := range []string{"cgroupfs", "systemd"} {
		for _, mode := range []string{"v1", "hybrid", "unified", "unified-submount"} {
			t.Run(driver+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				group := "kubepods/burstable/podaa-bb/deadbeef"
				if driver == "systemd" {
					group = "kubepods.slice/kubepods-burstable.slice/kubepods-burstable-podaa_bb.slice/cri-containerd-deadbeef.scope"
				}
				// These readable proc records cannot reveal host cgroup paths.
				writeHostFile(t, root, "proc/77/cgroup", "0::/\n")
				writeHostFile(t, root, "proc/1/mountinfo", "30 20 0:1 /../../../.. /sys/fs/cgroup rw - cgroup2 cgroup rw\n")
				base := "sys/fs/cgroup/"
				version := 1
				if mode == "unified" || mode == "unified-submount" {
					version = 2
					if mode == "unified-submount" {
						base += "unified/"
					}
					writeHostFile(t, root, base+"cgroup.controllers", "cpu memory\n")
					writeHostFile(t, root, base+group+"/cpu.stat", "usage_usec 7\nthrottled_usec 3\nnr_periods 5\nnr_throttled 2\n")
					writeHostFile(t, root, base+group+"/memory.current", "13\n")
				} else {
					writeHostFile(t, root, base+"cpu,cpuacct/"+group+"/cpuacct.usage", "7000\n")
					writeHostFile(t, root, base+"cpu,cpuacct/"+group+"/cpu.stat",
						"throttled_time 3000\nnr_periods 5\nnr_throttled 2\n")
					writeHostFile(t, root, base+"memory/"+group+"/memory.usage_in_bytes", "13\n")
					for _, controller := range []string{"cpu", "cpuacct"} {
						if err := os.Symlink("cpu,cpuacct", filepath.Join(root, base, controller)); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "hybrid" {
						// Jetson's v2 view has CPU usage, but no throttle/memory counters.
						writeHostFile(t, root, base+"unified/cgroup.controllers", "\n")
						writeHostFile(t, root, base+"unified/"+group+"/cpu.stat", "usage_usec 999\n")
					}
				}
				writeHostFile(t, root, "sys/class/thermal/thermal_zone4/type", "chip\n")
				writeHostFile(t, root, "sys/class/thermal/thermal_zone4/temp", "42000\n")
				at := time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)
				sample := SampleResources(root, []serving.ResourceTarget{target}, at)
				if len(sample.Containers) != 1 {
					t.Fatalf("sample=%+v", sample)
				}
				row := sample.Containers[0]
				if row.ResourceTarget != target || row.CgroupVersion != version ||
					row.CPUUsageNanos == nil || *row.CPUUsageNanos != 7000 ||
					row.ThrottledNanos == nil || *row.ThrottledNanos != 3000 ||
					row.Periods == nil || *row.Periods != 5 ||
					row.ThrottledPeriods == nil || *row.ThrottledPeriods != 2 ||
					row.MemoryCurrentBytes == nil || *row.MemoryCurrentBytes != 13 {
					t.Fatalf("sample=%+v container=%+v", sample, row)
				}
				if row.MemoryPeakBytes != nil || len(row.Errors) != 1 || row.Errors[0].Field != "memoryPeakBytes" {
					t.Fatalf("missing peak was fabricated: %+v", row)
				}
				if sample.SampledAt != at || len(sample.Errors) != 0 ||
					len(sample.Thermal) != 1 || sample.Thermal[0].MilliCelsius != 42000 {
					t.Fatalf("timestamp/thermal/errors=%+v", sample)
				}
			})
		}
	}
}

// A namespace-relative process record, a partial identity, or a different pod
// must never attribute root/other-container counters to the requested target.
func TestResourceCgroupMissingIdentity(t *testing.T) {
	target := serving.ResourceTarget{PodUID: "aa-bb", ContainerID: "containerd://deadbeef"}
	for _, group := range []string{
		"", "kubepods/burstable/podaa-bb/deadbeef1", "kubepods/burstable/podaa-bb1/deadbeef",
		"kubepods/burstable/podother/deadbeef", "podaa-bb/deadbeef",
		"kubepods.slice/kubepods-podaa_bb.slice/cri-containerd-deadbeef1.scope",
	} {
		t.Run(group, func(t *testing.T) {
			root := t.TempDir()
			writeHostFile(t, root, "sys/fs/cgroup/cgroup.controllers", "cpu memory\n")
			writeHostFile(t, root, filepath.Join("sys/fs/cgroup", group, "cpu.stat"), "usage_usec 999\n")
			writeHostFile(t, root, "proc/77/cgroup", "0::/\n")
			sample := SampleResources(root, []serving.ResourceTarget{target}, time.Now().UTC())
			if len(sample.Containers) != 0 {
				t.Fatalf("unmatched counters accepted: %+v", sample.Containers)
			}
			for _, failure := range sample.Errors {
				if failure.Target == target.PodUID+"/"+target.ContainerID && failure.Field == "cgroup" {
					return
				}
			}
			t.Fatalf("missing host cgroup not reported: %+v", sample.Errors)
		})
	}
}

func TestResourceCgroupHybridMissingControllers(t *testing.T) {
	target := serving.ResourceTarget{PodUID: "aa-bb", ContainerID: "containerd://deadbeef"}
	for _, memoryPresent := range []bool{false, true} {
		t.Run(fmt.Sprint(memoryPresent), func(t *testing.T) {
			root := t.TempDir()
			base := "sys/fs/cgroup/"
			group := "kubepods/podaa-bb/deadbeef/"
			writeHostFile(t, root, base+"cpu,cpuacct/cgroup.procs", "")
			writeHostFile(t, root, base+"unified/cgroup.controllers", "cpu memory\n")
			writeHostFile(t, root, base+"unified/"+group+"cpu.stat", "usage_usec 999\n")
			if memoryPresent {
				writeHostFile(t, root, base+"memory/"+group+"memory.usage_in_bytes", "13\n")
			}
			sample := SampleResources(root, []serving.ResourceTarget{target}, time.Now().UTC())
			if !memoryPresent {
				if len(sample.Containers) != 0 {
					t.Fatalf("hybrid fell back to v2: %+v", sample)
				}
				return
			}
			if len(sample.Containers) != 1 {
				t.Fatalf("missing partial v1 sample: %+v", sample)
			}
			row := sample.Containers[0]
			if row.CgroupVersion != 1 || row.CPUUsageNanos != nil || row.ThrottledNanos != nil ||
				row.MemoryCurrentBytes == nil || *row.MemoryCurrentBytes != 13 || len(row.Errors) == 0 {
				t.Fatalf("missing v1 controllers fabricated or replaced: %+v", row)
			}
		})
	}
}

func TestResourceCgroupSplitControllersAndTargets(t *testing.T) {
	root := t.TempDir() // No /proc mount is needed for discovery.
	targets := []serving.ResourceTarget{
		{PodUID: "aa-bb", ContainerID: "docker://deadbeef"},
		{PodUID: "aa-bb", ContainerID: "cri-o://other"},
		{PodUID: "cc-dd", ContainerID: "containerd://gone"},
	}
	for i, container := range []string{"docker-deadbeef.scope", "crio-other.scope"} {
		group := "kubepods.slice/kubepods-podaa_bb.slice/" + container + "/"
		writeHostFile(t, root, "sys/fs/cgroup/cpuacct/"+group+"cpuacct.usage", fmt.Sprint(7000+i))
		writeHostFile(t, root, "sys/fs/cgroup/cpu/"+group+"cpu.stat", "throttled_time 3000\nnr_periods 5\nnr_throttled 2\n")
		writeHostFile(t, root, "sys/fs/cgroup/memory/"+group+"memory.usage_in_bytes", "13\n")
		writeHostFile(t, root, "sys/fs/cgroup/memory/"+group+"memory.max_usage_in_bytes", "17\n")
	}
	sample := SampleResources(root, targets, time.Now().UTC())
	if len(sample.Containers) != 2 {
		t.Fatalf("sample=%+v", sample)
	}
	for i, row := range sample.Containers {
		if row.ResourceTarget != targets[i] || row.CgroupVersion != 1 || len(row.Errors) != 0 ||
			row.CPUUsageNanos == nil || *row.CPUUsageNanos != uint64(7000+i) ||
			row.MemoryPeakBytes == nil || *row.MemoryPeakBytes != 17 {
			t.Fatalf("target %d misattributed: %+v", i, row)
		}
	}
}

func writeHostFile(t *testing.T, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
