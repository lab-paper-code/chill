package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// SampleResources resolves cgroups from host process and mount records. It never
// constructs a systemd/cgroupfs layout from Kubernetes identities.
func SampleResources(root string, targets []serving.ResourceTarget, at time.Time) serving.ResourceSample {
	sample := serving.ResourceSample{
		SampledAt: at, Containers: []serving.ContainerResources{}, Thermal: []serving.ThermalResource{},
	}
	processes, err := os.ReadDir(filepath.Join(root, "proc"))
	if err != nil {
		sample.Errors = append(sample.Errors, resourceError("host", "proc", err))
	}
	sample.Containers = make([]serving.ContainerResources, len(targets))
	found := make([]bool, len(targets))
	for i, target := range targets {
		sample.Containers[i].ResourceTarget = target
	}
	for _, process := range processes {
		if _, err := strconv.ParseUint(process.Name(), 10, 64); err != nil {
			continue
		}
		base := filepath.Join(root, "proc", process.Name())
		cg, err := os.ReadFile(filepath.Join(base, "cgroup"))
		if err != nil {
			continue
		}
		var mounts []byte
		for i, target := range targets {
			if found[i] || !matchesResourceIdentity(string(cg), target) {
				continue
			}
			row := &sample.Containers[i]
			if mounts == nil {
				mounts, err = os.ReadFile(filepath.Join(base, "mountinfo"))
				if err != nil {
					row.Errors = append(row.Errors, resourceError(target.ContainerID, "mountinfo", err))
					continue
				}
			}
			paths, version := resourceCgroupPaths(root, string(cg), string(mounts))
			if version == 0 {
				continue
			}
			row.CgroupVersion = version
			readContainerResources(row, paths)
			found[i] = true
		}
	}
	count := 0
	for i, target := range targets {
		if !found[i] {
			sample.Errors = append(sample.Errors, sample.Containers[i].Errors...)
			sample.Errors = append(sample.Errors, serving.ResourceError{
				Target: target.PodUID + "/" + target.ContainerID, Field: "cgroup",
				Reason: "matching Pod UID/containerID cgroup and mount not found",
			})
			continue
		}
		sample.Containers[count] = sample.Containers[i]
		count++
	}
	sample.Containers = sample.Containers[:count]
	zones, err := filepath.Glob(filepath.Join(root, "sys/class/thermal/thermal_zone*"))
	if err != nil || len(zones) == 0 {
		sample.Errors = append(sample.Errors, serving.ResourceError{
			Target: "host", Field: "thermal", Reason: "thermal sensors unavailable",
		})
	}
	for _, zone := range zones {
		kind, err := os.ReadFile(filepath.Join(zone, "type"))
		if err != nil {
			sample.Errors = append(sample.Errors, resourceError(zone, "type", err))
			continue
		}
		raw, err := os.ReadFile(filepath.Join(zone, "temp"))
		if err != nil {
			sample.Errors = append(sample.Errors, resourceError(zone, "temperature", err))
			continue
		}
		temperature, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			sample.Errors = append(sample.Errors, resourceError(zone, "temperature", err))
			continue
		}
		sensor := strings.TrimPrefix(zone, root) + ":" + strings.TrimSpace(string(kind))
		sample.Thermal = append(sample.Thermal, serving.ThermalResource{Sensor: sensor, MilliCelsius: temperature})
	}
	return sample
}

func matchesResourceIdentity(records string, target serving.ResourceTarget) bool {
	_, id, ok := strings.Cut(target.ContainerID, "://")
	if !ok || id == "" || target.PodUID == "" {
		return false
	}
	for _, record := range strings.Split(records, "\n") {
		fields := strings.SplitN(record, ":", 3)
		if len(fields) != 3 {
			continue
		}
		// Runtime IDs may be scope-decorated; require token boundaries rather than
		// a short substring that could identify a different container.
		normalized := strings.NewReplacer("_", "-", "/", " ", ".", " ").Replace(fields[2])
		uid := strings.ReplaceAll(target.PodUID, "_", "-")
		hasUID, hasID := false, false
		for _, component := range strings.Fields(normalized) {
			hasUID = hasUID || component == uid || component == "pod"+uid || strings.HasSuffix(component, "-pod"+uid)
			hasID = hasID || component == id || strings.HasSuffix(component, "-"+id)
		}
		if hasUID && hasID {
			return true
		}
	}
	return false
}

func resourceCgroupPaths(root, records, mounts string) (map[string]string, int) {
	paths := map[string]string{}
	version := 0
	for _, line := range strings.Split(mounts, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		left, right := strings.Fields(before), strings.Fields(after)
		if len(left) < 5 || len(right) < 3 || (right[0] != "cgroup" && right[0] != "cgroup2") {
			continue
		}
		mountRoot, mountPoint := unescapeMount(left[3]), unescapeMount(left[4])
		for _, record := range strings.Split(records, "\n") {
			fields := strings.SplitN(record, ":", 3)
			if len(fields) != 3 {
				continue
			}
			v2 := right[0] == "cgroup2" && fields[0] == "0" && fields[1] == ""
			controllers := strings.Split(fields[1], ",")
			if !v2 && right[0] != "cgroup" {
				continue
			}
			rel, err := filepath.Rel(mountRoot, fields[2])
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				continue
			}
			// Only read the host /sys mounts, not arbitrary process-provided paths.
			if mountPoint != "/sys" && !strings.HasPrefix(mountPoint, "/sys/") {
				continue
			}
			path := filepath.Join(root, mountPoint, rel)
			if v2 {
				paths["unified"] = path
				version = 2
				continue
			}
			for _, controller := range controllers {
				for _, option := range strings.Split(right[2], ",") {
					if option == controller {
						paths[controller] = path
						if version == 0 {
							version = 1
						}
					}
				}
			}
		}
	}
	return paths, version
}
func unescapeMount(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}
func resourceError(target, field string, err error) serving.ResourceError {
	return serving.ResourceError{Target: target, Field: field, Reason: err.Error()}
}

func readContainerResources(row *serving.ContainerResources, paths map[string]string) {
	read := func(controller, file, field string) *uint64 {
		path := paths[controller]
		if path == "" {
			row.Errors = append(row.Errors, serving.ResourceError{
				Target: row.ContainerID, Field: field, Reason: "controller unavailable",
			})
			return nil
		}
		raw, err := os.ReadFile(filepath.Join(path, file))
		if err != nil {
			row.Errors = append(row.Errors, resourceError(row.ContainerID, field, err))
			return nil
		}
		value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			row.Errors = append(row.Errors, resourceError(row.ContainerID, field, err))
			return nil
		}
		return &value
	}
	stat := func(controller string, keys map[string]**uint64, scales map[string]uint64) {
		if paths[controller] == "" {
			for field := range keys {
				row.Errors = append(row.Errors, serving.ResourceError{
					Target: row.ContainerID, Field: field, Reason: "controller unavailable",
				})
			}
			return
		}
		raw, err := os.ReadFile(filepath.Join(paths[controller], "cpu.stat"))
		values := map[string]string{}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				values[fields[0]] = fields[1]
			}
		}
		for key, destination := range keys {
			value, parseErr := strconv.ParseUint(values[key], 10, 64)
			if err != nil {
				parseErr = err
			}
			scale := scales[key]
			if scale == 0 {
				scale = 1
			}
			if parseErr == nil && value > math.MaxUint64/scale {
				parseErr = fmt.Errorf("counter overflow")
			}
			if parseErr != nil {
				row.Errors = append(row.Errors, resourceError(row.ContainerID, key, parseErr))
				continue
			}
			value *= scale
			*destination = &value
		}
	}
	if row.CgroupVersion == 2 {
		stat("unified", map[string]**uint64{
			"usage_usec": &row.CPUUsageNanos, "throttled_usec": &row.ThrottledNanos,
			"nr_periods": &row.Periods, "nr_throttled": &row.ThrottledPeriods,
		}, map[string]uint64{"usage_usec": 1000, "throttled_usec": 1000})
		row.MemoryCurrentBytes = read("unified", "memory.current", "memoryCurrentBytes")
		row.MemoryPeakBytes = read("unified", "memory.peak", "memoryPeakBytes")
	} else {
		row.CPUUsageNanos = read("cpuacct", "cpuacct.usage", "cpuUsageNanos")
		stat("cpu", map[string]**uint64{
			"throttled_time": &row.ThrottledNanos, "nr_periods": &row.Periods,
			"nr_throttled": &row.ThrottledPeriods,
		}, nil)
		row.MemoryCurrentBytes = read("memory", "memory.usage_in_bytes", "memoryCurrentBytes")
		row.MemoryPeakBytes = read("memory", "memory.max_usage_in_bytes", "memoryPeakBytes")
	}
}
