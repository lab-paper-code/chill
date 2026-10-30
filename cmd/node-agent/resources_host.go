package main

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// SampleResources resolves Kubernetes identities against the read-only host
// cgroupfs tree; /proc paths may be relative to the agent's cgroup namespace.
func SampleResources(root string, targets []serving.ResourceTarget, at time.Time) serving.ResourceSample {
	sample := serving.ResourceSample{
		SampledAt: at, Containers: []serving.ContainerResources{}, Thermal: []serving.ThermalResource{},
	}
	paths, version, failures := resourceCgroupPaths(root, targets)
	sample.Errors = append(sample.Errors, failures...)
	for i, target := range targets {
		if len(paths[i]) == 0 {
			sample.Errors = append(sample.Errors, serving.ResourceError{
				Target: target.PodUID + "/" + target.ContainerID, Field: "cgroup",
				Reason: "matching Pod UID/containerID directory not found in host cgroupfs",
			})
			continue
		}
		row := serving.ContainerResources{ResourceTarget: target, CgroupVersion: version}
		readContainerResources(&row, paths[i])
		sample.Containers = append(sample.Containers, row)
	}
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

func resourceCgroupPaths(
	root string, targets []serving.ResourceTarget,
) ([]map[string]string, int, []serving.ResourceError) {
	paths := make([]map[string]string, len(targets))
	if len(targets) == 0 {
		return paths, 0, nil
	}
	base := filepath.Join(root, "sys/fs/cgroup")
	entries, err := os.ReadDir(base)
	if err != nil {
		return paths, 0, []serving.ResourceError{resourceError("host", "cgroup", err)}
	}
	hierarchies, version := resourceCgroupHierarchies(base, entries)
	var failures []serving.ResourceError
	for hierarchy, controllers := range hierarchies {
		err := filepath.WalkDir(hierarchy, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				failures = append(failures, resourceError(path, "cgroup", walkErr))
				return filepath.SkipDir
			}
			if !entry.IsDir() || path == hierarchy {
				return nil
			}
			if filepath.Dir(path) == hierarchy && entry.Name() != "kubepods" && entry.Name() != "kubepods.slice" {
				return filepath.SkipDir
			}
			switch entry.Name() {
			case "kubepods", "kubepods.slice", "burstable", "besteffort",
				"kubepods-burstable.slice", "kubepods-besteffort.slice":
				return nil
			}
			for i, target := range targets {
				if !matchesResourcePod(entry.Name(), target.PodUID) {
					continue
				}
				containers, err := os.ReadDir(path)
				if err != nil {
					failures = append(failures, resourceError(path, "cgroup", err))
					continue
				}
				for _, container := range containers {
					if !container.IsDir() || !matchesResourceContainer(container.Name(), target.ContainerID) {
						continue
					}
					if paths[i] == nil {
						paths[i] = map[string]string{}
					}
					for _, controller := range controllers {
						paths[i][controller] = filepath.Join(path, container.Name())
					}
				}
			}
			return filepath.SkipDir
		})
		if err != nil {
			failures = append(failures, resourceError(hierarchy, "cgroup", err))
		}
	}
	return paths, version, failures
}

func resourceCgroupHierarchies(base string, entries []os.DirEntry) (map[string][]string, int) {
	// A unified mount exposes cgroup.controllers at its root. Otherwise the
	// directory contains v1 controller mounts, possibly alongside unified.
	hierarchies := map[string][]string{}
	if _, err := os.Stat(filepath.Join(base, "cgroup.controllers")); err == nil {
		hierarchies[base] = []string{"unified"}
		return hierarchies, 2
	}
	hasV1 := false
	for _, entry := range entries {
		if entry.Name() == "unified" {
			continue
		}
		path := filepath.Join(base, entry.Name())
		info, err := os.Stat(path) // v1 commonly has cpu/cpuacct symlink aliases.
		if err != nil || !info.IsDir() {
			continue
		}
		hasV1 = true
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		for _, controller := range strings.Split(entry.Name(), ",") {
			if controller == "cpu" || controller == "cpuacct" || controller == "memory" {
				hierarchies[resolved] = append(hierarchies[resolved], controller)
			}
		}
	}
	// Never use the hybrid v2 CPU-only view in place of missing v1 data.
	if !hasV1 {
		path := filepath.Join(base, "unified")
		if _, err := os.Stat(filepath.Join(path, "cgroup.controllers")); err == nil {
			hierarchies[path] = []string{"unified"}
			return hierarchies, 2
		}
	}
	return hierarchies, 1
}

func matchesResourcePod(name, uid string) bool {
	if uid == "" {
		return false
	}
	if name == "pod"+uid {
		return true
	}
	uid = strings.ReplaceAll(uid, "-", "_")
	return name == "kubepods-pod"+uid+".slice" ||
		name == "kubepods-burstable-pod"+uid+".slice" ||
		name == "kubepods-besteffort-pod"+uid+".slice"
}

func matchesResourceContainer(name, containerID string) bool {
	_, id, ok := strings.Cut(containerID, "://")
	return ok && id != "" && (name == id || name == "cri-containerd-"+id+".scope" ||
		name == "crio-"+id+".scope" || name == "docker-"+id+".scope")
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
