package live

import (
	"testing"
	"time"
)

func TestClusterEventRequiresExactlyDeclaredPayload(t *testing.T) {
	at := time.Now().UTC()
	cluster := &ClusterEvent{Kind: "issue", At: timestamp(at), Event: "raised", Source: "pod/container", Reason: "unready"}
	for _, event := range []*Event{
		{Kind: "cluster"},
		{Kind: "cluster", Cluster: cluster, Decision: &Decision{}},
		{Kind: "action", Action: &ActionEvent{}, Cluster: cluster},
		{Kind: "decision", Decision: &Decision{}, Cluster: cluster},
	} {
		if err := New(at).Publish(at, Delta{}, event); err == nil {
			t.Fatalf("accepted mismatched event: %+v", event)
		}
	}
	if err := New(at).Publish(at, Delta{}, &Event{Kind: "cluster", Cluster: cluster}); err != nil {
		t.Fatal(err)
	}
}
