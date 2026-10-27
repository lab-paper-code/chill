package serving

// Current captures definition/admission, physical instance state and effective
// routing under the same lock. The returned collections belong to the reader.
func (r *Runtime) Current() ([]WorkloadState, []InstanceState, map[string]map[string]int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	states := make([]WorkloadState, 0, len(r.workloads))
	for _, w := range r.workloads {
		if !w.detached {
			states = append(states, r.workloadStateLocked(w))
		}
	}
	routing, _ := r.schedulerRoutingLocked()
	return states, r.instancesLocked(), routing
}
