package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// SCH-AC-53: identities and actual mount records, not a guessed runtime layout.
func TestResourceCgroupVersionsAndMissingPeak(t *testing.T) {
	target := serving.ResourceTarget{PodUID: "aa-bb", ContainerID: "containerd://deadbeef"}
	for _, version := range []int{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			root := t.TempDir()
			group := "/arbitrary/podaa-bb/deadbeef"
			if version == 2 {
				writeHostFile(t, root, "proc/77/cgroup", "0::"+group+"\n")
				writeHostFile(t, root, "proc/1/mountinfo", "30 20 0:1 / /sys/custom rw - cgroup2 cgroup rw\n")
				writeHostFile(t, root, "sys/custom"+group+"/cpu.stat",
					"usage_usec 7\nthrottled_usec 3\nnr_periods 5\nnr_throttled 2\n")
				writeHostFile(t, root, "sys/custom"+group+"/memory.current", "13\n")
			} else {
				writeHostFile(t, root, "proc/77/cgroup", "2:cpu,cpuacct:"+group+"\n3:memory:"+group+"\n")
				writeHostFile(t, root, "proc/1/mountinfo",
					"30 20 0:1 / /sys/unusual/cpu rw - cgroup cgroup rw,cpu,cpuacct\n"+
						"31 20 0:2 / /sys/unusual/memory rw - cgroup cgroup rw,memory\n")
				writeHostFile(t, root, "sys/unusual/cpu"+group+"/cpuacct.usage", "7000\n")
				writeHostFile(t, root, "sys/unusual/cpu"+group+"/cpu.stat", "throttled_time 3000\nnr_periods 5\nnr_throttled 2\n")
				writeHostFile(t, root, "sys/unusual/memory"+group+"/memory.usage_in_bytes", "13\n")
			}
			writeHostFile(t, root, "sys/class/thermal/thermal_zone4/type", "chip\n")
			writeHostFile(t, root, "sys/class/thermal/thermal_zone4/temp", "42000\n")
			at := time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)
			sample := SampleResources(root, []serving.ResourceTarget{target}, at)
			row := sample.Containers[0]
			if row.CgroupVersion != version || row.CPUUsageNanos == nil || *row.CPUUsageNanos != 7000 ||
				row.ThrottledNanos == nil || *row.ThrottledNanos != 3000 ||
				row.MemoryCurrentBytes == nil || *row.MemoryCurrentBytes != 13 {
				t.Fatalf("sample=%+v container=%+v", sample, row)
			}
			if row.MemoryPeakBytes != nil || len(row.Errors) == 0 {
				t.Fatalf("missing peak was fabricated: %+v", row)
			}
			if sample.SampledAt != at || len(sample.Thermal) != 1 || sample.Thermal[0].MilliCelsius != 42000 {
				t.Fatalf("timestamp/thermal=%+v", sample)
			}
		})
	}
	if matchesResourceIdentity("0::/podaa-bb/deadbeef1\n", target) {
		t.Fatal("partial container identity accepted")
	}
	if !matchesResourceIdentity("0::/kubepods-burstable-podaa_bb.slice/cri-containerd-deadbeef.scope\n", target) {
		t.Fatal("systemd identity not discovered")
	}
}

// SCH-AC-53: a target's subtree mount must never alias host root counters.
func TestResourceCgroupTargetSubtreeMount(t *testing.T) {
	target := serving.ResourceTarget{PodUID: "aa-bb", ContainerID: "containerd://deadbeef"}
	group := "/arbitrary/podaa-bb/deadbeef"
	for _, present := range []bool{false, true} {
		name := "missing-host-directory"
		if present {
			name = "present-host-directory"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeHostFile(t, root, "proc/77/cgroup", "0::"+group+"\n")
			writeHostFile(t, root, "proc/77/mountinfo",
				"30 20 0:1 "+group+" /sys/fs/cgroup rw - cgroup2 cgroup rw\n")
			writeHostFile(t, root, "proc/1/mountinfo",
				"30 20 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n")
			writeHostFile(t, root, "sys/fs/cgroup/cpu.stat",
				"usage_usec 999\nthrottled_usec 999\nnr_periods 999\nnr_throttled 999\n")
			if present {
				writeHostFile(t, root, "sys/fs/cgroup"+group+"/cpu.stat",
					"usage_usec 7\nthrottled_usec 3\nnr_periods 5\nnr_throttled 2\n")
			}
			sample := SampleResources(root, []serving.ResourceTarget{target}, time.Now().UTC())
			if !present {
				if len(sample.Containers) != 0 {
					t.Fatalf("missing host cgroup accepted host root counters: %+v", sample.Containers)
				}
				for _, failure := range sample.Errors {
					if failure.Target == target.PodUID+"/"+target.ContainerID && failure.Field == "cgroup" {
						return
					}
				}
				t.Fatalf("missing host cgroup not reported: %+v", sample.Errors)
			}
			if len(sample.Containers) != 1 || sample.Containers[0].CPUUsageNanos == nil ||
				*sample.Containers[0].CPUUsageNanos != 7000 {
				t.Fatalf("target subtree misattributed: %+v", sample.Containers)
			}
		})
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
