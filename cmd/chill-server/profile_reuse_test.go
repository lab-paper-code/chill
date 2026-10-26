package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMeasuredReusePreparationKeepsTargetIdentity(t *testing.T) {
	service := &schedulerService{
		kube: fake.NewSimpleClientset(&core.Node{ObjectMeta: meta.ObjectMeta{Name: "genesis3", UID: "target-uid"}}),
		opts: options{
			storeName: "store", storeEndpoint: "http://store", storeBucket: "bucket", storeRegion: "region",
			storeCredentialsSecret: "actual-secret", operationTimeout: time.Hour, maxRequestBytes: 8 << 20,
		},
	}
	profile := measuredAdoptionProfile()
	profile.ID = "measurements/confirmation/uid/reuse"
	profile.NodeName, profile.Condition.NodeUID = "genesis3", "target-uid"
	profile.RuntimeMetadata.GPUUUID = "GPU-target"
	profile.Reuse = &serving.ProfileReuseCheck{
		SourceProfileID: "measurements/original/uid", ConfirmationProfileID: "measurements/confirmation/uid",
		SourceNode: serving.NodeReference{Name: "genesis2", UID: "source-uid"},
		TargetNode: serving.NodeReference{Name: "genesis3", UID: "target-uid"}, Outcome: "Accepted",
	}
	spec, err := service.measuredPreparation(context.Background(), profile, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	command := strings.Join(spec.Config.RunnerCommand, " ")
	if spec.Instance.Node != profile.Reuse.TargetNode || spec.Instance.Allocation.GPUUUID != "GPU-target" ||
		spec.Config.Policy.ProfileID != profile.ID || !strings.Contains(command, "--gpu-uuid GPU-target") ||
		strings.Contains(command, "source-uid") {
		t.Fatalf("reuse bypassed the physical preparation contract: %+v", spec)
	}
	profile.Condition.NodeUID = "source-uid"
	if _, err := service.measuredPreparation(context.Background(), profile, 128<<20); err == nil {
		t.Fatal("source UID alias bypassed the current target Node guard")
	}
}

func TestProfileReuseRetainsAcceptedAndRejectedResults(t *testing.T) {
	type stored struct {
		path string
		body []byte
	}
	writes := make(chan stored, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" {
			t.Errorf("comparison retention must use immutable PUT: %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writes <- stored{r.URL.Path, raw}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	objects, err := stores3.New(stores3.Config{LogicalStore: "evidence", Bucket: "bucket",
		Endpoint: server.URL, Region: "region", AccessKeyID: "test", SecretAccessKey: "test", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	service := &schedulerService{objects: objects, opts: options{storeName: "evidence", maxEvidenceBytes: 1 << 20}}
	accepted := serving.ProfileReuseCheck{
		SourceProfileID: "ns/source/uid", ConfirmationProfileID: "ns/accepted/uid", Outcome: "Accepted",
	}
	rejected := serving.ProfileReuseCheck{SourceProfileID: "ns/source/uid", ConfirmationProfileID: "ns/rejected/uid",
		Outcome: "Rejected", Reason: "wall power difference"}
	view := servingkube.CatalogView{Profiles: []serving.Profile{{ID: "target/reuse-hash", Reuse: &accepted}},
		ReuseChecks: []serving.ProfileReuseCheck{accepted, rejected}}
	if err := service.retainProfileReuseChecks(context.Background(), &view); err != nil {
		t.Fatal(err)
	}
	first, second := <-writes, <-writes
	for index, item := range []stored{first, second} {
		var record struct {
			SchemaVersion string                    `json:"schemaVersion"`
			Check         serving.ProfileReuseCheck `json:"check"`
		}
		if err := json.Unmarshal(item.body, &record); err != nil ||
			record.SchemaVersion != "chill.dacs.io/profile-reuse-check.v1alpha1" ||
			record.Check.Outcome != view.ReuseChecks[index].Outcome || record.Check.Record != nil ||
			view.ReuseChecks[index].Record == nil || item.path != "/bucket/"+view.ReuseChecks[index].Record.Key {
			t.Fatalf("comparison result was not durably linked: %s %v", item.body, err)
		}
	}
	if view.Profiles[0].Reuse.Record == nil || *view.Profiles[0].Reuse.Record != *view.ReuseChecks[0].Record {
		t.Fatal("accepted planner profile lacks its retained comparison record")
	}
	if err := service.retainProfileReuseChecks(context.Background(), &view); err != nil {
		t.Fatal(err)
	}
	for _, original := range []stored{first, second} {
		repeated := <-writes
		if original.path != repeated.path || string(original.body) != string(repeated.body) {
			t.Fatal("record references made repeated retention non-idempotent")
		}
	}
	service.opts.maxEvidenceBytes = 1
	if err := service.retainProfileReuseChecks(context.Background(), &view); err == nil {
		t.Fatal("unbounded comparison record accepted")
	}
}
