package serving

import "testing"

func TestCurrentSnapshotKeepsExistingChangeSubscriber(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	notified := make(chan struct{}, 1)
	r.config.OnChange = func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	}
	r.mu.Lock()
	r.signal()
	r.mu.Unlock()
	select {
	case <-notified:
	default:
		t.Fatal("live listener was not notified")
	}
	select {
	case <-r.Changed():
	default:
		t.Fatal("live listener consumed the existing observer notification")
	}
	states, instances, routing := r.Current()
	if len(states) == 0 || len(instances) != 1 || instances[0].ID != instance.ID || routing["w1"][instance.ID] != 1 {
		t.Fatalf("uncoupled live view: %+v %+v %+v", states, instances, routing)
	}
	state, err := r.State(states[0].Applied.Name)
	if err != nil || state.Applied != states[0].Applied || state.Admission != states[0].Admission || state.Reason != states[0].Reason {
		t.Fatalf("live state disagrees with public state: %+v %v", state, err)
	}
	// Returned collection edits cannot change the effective runtime distribution.
	routing["w1"][instance.ID] = 0
	instances[0].Routable["w1"] = false
	_, _, after := r.Current()
	if after["w1"][instance.ID] != 1 {
		t.Fatal("reader mutated live routing")
	}
}
