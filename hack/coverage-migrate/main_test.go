package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Reuse the real catalog's valid synthetic fixture instead of maintaining a
// second mock scientific record. Only trial-0 and its BEO are retired to the old
// contract; the other trials supply the required active/idle baseline.
func TestSyntheticCatalogMigration(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := os.MkdirTemp(filepath.Join(root, "hack"), ".covmig-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(scratch)
	archive := exec.Command("git", "archive", "9ccff96e")
	archive.Dir = root
	rd, err := archive.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = archive.Start(); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(rd)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		p := filepath.Join(scratch, h.Name)
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(f, tr)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = archive.Wait(); err != nil {
		t.Fatal(err)
	}
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var fixture struct {
			Objects map[string][]byte `json:"objects"`
			Lists   map[string]object `json:"lists"`
		}
		if err := json.NewDecoder(r.Body).Decode(&fixture); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.URL.Path, "/bucket/")
			b, ok := fixture.Objects[key]
			if !ok {
				http.Error(w, "<Error><Code>NoSuchKey</Code></Error>", 404)
				return
			}
			_, _ = w.Write(b)
		}))
		defer s3.Close()
		store, err := stores3.New(stores3.Config{LogicalStore: "evidence", Bucket: "bucket", Region: "us-east-1", Endpoint: s3.URL, AccessKeyID: "synthetic", SecretAccessKey: "synthetic"})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		m := &migrator{store: store, lists: fixture.Lists, mappings: map[string]conversion{}}
		m.prepare()
		if len(m.failures) > 0 {
			http.Error(w, strings.Join(m.failures, "\n"), 500)
			return
		}
		for _, c := range m.mappings {
			b, err := m.converted(c)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if len(b) != int(c.New.Size) {
				http.Error(w, "new reference size mismatch", 500)
				return
			}
			fixture.Objects[c.New.Key] = b
		}
		if m.executionCount != 1 || m.rawCount != 1 || m.planCount != 1 || m.resultCount != 1 {
			http.Error(w, "expected exactly one retired Run and BEO", 500)
			return
		}
		m.backupPath = filepath.Join(scratch, "synthetic-status.json")
		if err = m.saveBackup(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err = m.saveBackup(); err != nil {
			http.Error(w, "matching backup was not reusable: "+err.Error(), 500)
			return
		}
		retry := &migrator{store: store, lists: fixture.Lists, mappings: map[string]conversion{}}
		retry.prepare()
		if len(retry.changes) != 0 || len(retry.mappings) != 0 {
			http.Error(w, "already-current statuses were not skipped", 500)
			return
		}
		// The production roots all end in .json; the repository fixture's Run
		// keys do not, so exercise root rewind with its canonical BEO root.
		replay := map[string]object{"profilingruns": {"items": encode([]object{})}}
		replay["batchenergyoptimizations"], _ = decode(encode(fixture.Lists["batchenergyoptimizations"]))
		if err = m.originalStatuses(replay); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if strings.Contains(string(replay["batchenergyoptimizations"]["items"]), "shared-cpu") {
			http.Error(w, "original BEO root was not recovered", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(fixture)
	}))
	defer callback.Close()
	if err = os.WriteFile(filepath.Join(scratch, "internal/serving/kube/covmig_synthetic_test.go"), []byte(syntheticCatalogCheck), 0600); err != nil {
		t.Fatal(err)
	}
	check := exec.Command("go", "test", "./internal/serving/kube", "-run", "^TestOneRetiredRunAndBEO$", "-count=1")
	check.Dir = scratch
	check.Env = append(os.Environ(), "COVMIG_FIXTURE="+callback.URL, "GOTMPDIR="+scratch)
	output, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("catalog migration: %v\n%s", err, output)
	}
	// json.RawMessage preserves numeric lexemes, including exponent and trailing zeroes.
	b := []byte(`{"schemaVersion":"chill.dacs.io/profiling-raw-measurement.v1alpha8","runConfig":{"schemaVersion":"chill.dacs.io/profiling-run-config.v1alpha17","cpu":{}},"runtime":{"schemaVersion":"chill.dacs.io/batch-characterization.v1alpha8","cpuContract":{"cgroupCPUMax":"400000 100000","ortIntraOpThreads":4,"ortInterOpThreads":1,"processCPUSeconds":1.2300e+04},"repetitionResults":[{"latencyMs":[0.000100,2e-03]}]}}`)
	converted, err := transform(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range [][]byte{[]byte("1.2300e+04"), []byte("0.000100"), []byte("2e-03")} {
		if !bytes.Contains(converted, token) {
			t.Fatalf("number changed: %s", token)
		}
	}
	old, _ := decode(b)
	next, _ := decode(converted)
	if !bytes.Equal(child(old, "runtime")["cpuContract"], child(next, "runtime")["cpuContract"]) {
		t.Fatal("runtime CPU observations changed")
	}
	full := []object{
		{"id": encode("old"), "measuredAt": encode("2026-10-22T00:00:00Z"), "condition": json.RawMessage(`{"runtimeImage":"same","cpu":{"limit":"1"}}`), "metricSource": encode(object{})},
		{"id": encode("new"), "measuredAt": encode("2026-10-24T00:00:00Z"), "condition": json.RawMessage(`{"runtimeImage":"same","cpu":{"limit":"4"}}`), "metricSource": encode(object{})},
	}
	selected, merges, err := expectedProfiles(full)
	if err != nil || len(selected) != 1 || !selected["new"] || merges["old"] != "new" {
		t.Fatal("non-latest CPU duplicate set is wrong", err, selected, merges)
	}
	full[1]["condition"] = json.RawMessage(`{"runtimeImage":"different","cpu":{"limit":"4"}}`)
	selected, merges, err = expectedProfiles(full)
	if err != nil || len(selected) != 2 || len(merges) != 0 {
		t.Fatal("runtime image was incorrectly ignored", err, selected, merges)
	}
}

const syntheticCatalogCheck = `package kube
import("bytes";"context";"encoding/json";"io";"net/http";"os";"reflect";"strings";"testing";api "github.com/lab-paper-code/chill/api/v1alpha1";"github.com/lab-paper-code/chill/internal/evidencestore";"sigs.k8s.io/controller-runtime/pkg/client/fake")
func TestOneRetiredRunAndBEO(t *testing.T){
 ctx:=context.Background();catalog,source,objects,plan:=catalogEvidenceFixture(t,4096)
 before,err:=catalog.Refresh(ctx,[]string{source.Namespace});if err!=nil||len(before.Profiles)!=1{t.Fatalf("fixture: %+v %v",before,err)}
 var runs api.ProfilingRunList;var campaigns api.BatchEnergyOptimizationList
 if err=catalog.store.client.List(ctx,&runs);err!=nil{t.Fatal(err)};if err=catalog.store.client.List(ctx,&campaigns);err!=nil{t.Fatal(err)}
 type obj=map[string]json.RawMessage
 dec:=func(b []byte)obj{var v obj;if err:=json.Unmarshal(b,&v);err!=nil{t.Fatal(err)};return v};enc:=func(v any)[]byte{b,err:=json.Marshal(v);if err!=nil{t.Fatal(err)};return b}
 oldCPU:=json.RawMessage("{\"policy\":\"ConfiguredCPUQuota\",\"policyVersion\":\"v1\",\"request\":\"100m\",\"limit\":\"4\",\"ortIntraOpThreads\":2,\"ortInterOpThreads\":1,\"exclusivityClaimed\":false}")
 for i:=range runs.Items{run:=&runs.Items[i];if run.Name!="trial-0"{continue};e:=run.Status.Evidence;execLoc:=evidencestore.ObjectLocation{Store:e.Store,Key:e.Key};execution:=dec(objects[execLoc]);rawID:=dec(execution["rawMeasurement"]);var rawKey string;_ = json.Unmarshal(rawID["key"],&rawKey);rawLoc:=evidencestore.ObjectLocation{Store:e.Store,Key:rawKey};raw:=dec(objects[rawLoc]);raw["schemaVersion"]=enc("chill.dacs.io/profiling-raw-measurement.v1alpha8")
  config:=dec(raw["runConfig"]);config["schemaVersion"]=enc("chill.dacs.io/profiling-run-config.v1alpha17");config["cpu"]=oldCPU;raw["runConfig"]=enc(config);execution["runConfig"]=enc(config)
  runtime:=dec(raw["runtime"]);runtime["schemaVersion"]=enc("chill.dacs.io/batch-characterization.v1alpha8");derived:=dec(runtime["derivedExecutionContract"]);derived["cpu"]=oldCPU;derived["runtimeOptions"]=json.RawMessage("{}");derived["scheduling"]=json.RawMessage("{}");runtime["derivedExecutionContract"]=enc(derived);raw["runtime"]=enc(runtime);objects[rawLoc]=enc(raw)
  rawID["size"]=enc(len(objects[rawLoc]));execution["rawMeasurement"]=enc(rawID);execution["schemaVersion"]=enc("chill.dacs.io/profiling-execution-evidence.v1alpha15");objects[execLoc]=enc(execution);e.Size=int64(len(objects[execLoc]));e.SchemaVersion="chill.dacs.io/profiling-execution-evidence.v1alpha15"
 }
 p:=dec(objects[plan.Location()]);p["schemaVersion"]=enc("chill.dacs.io/curve-plan.v1alpha9");shared:=dec(p["shared"]);shared["cpu"]=oldCPU;p["shared"]=enc(shared);objects[plan.Location()]=enc(p)
 campaign:=&campaigns.Items[0];resultLoc:=apiObjectIdentity(campaign.Status.Result).Location();result:=dec(objects[resultLoc]);planRef:=dec(result["curvePlan"]);planRef["size"]=enc(len(objects[plan.Location()]));planRef["schemaVersion"]=p["schemaVersion"];result["curvePlan"]=enc(planRef)
 var trials []obj;_ = json.Unmarshal(result["completedTrials"],&trials);for _,trial:=range trials{runRef:=dec(trial["run"]);if string(runRef["name"])=="\"trial-0\""{for _,run:=range runs.Items{if run.Name=="trial-0"{trial["evidence"]=enc(run.Status.Evidence)}}}};result["completedTrials"]=enc(trials);objects[resultLoc]=enc(result);campaign.Status.Result.Size=int64(len(objects[resultLoc]))
 payload:=struct{Objects map[string][]byte "json:\"objects\"";Lists map[string]obj "json:\"lists\""}{Objects:map[string][]byte{},Lists:map[string]obj{"profilingruns":dec(enc(runs)),"batchenergyoptimizations":dec(enc(campaigns)),"modelloadedidlebaselines":dec([]byte("{\"items\":[]}"))}}
 for loc,b:=range objects{payload.Objects[loc.Key]=b};res,err:=http.Post(os.Getenv("COVMIG_FIXTURE"),"application/json",bytes.NewReader(enc(payload)));if err!=nil{t.Fatal(err)};defer res.Body.Close();body,_:=io.ReadAll(res.Body);if res.StatusCode!=200{t.Fatal(string(body))};if err=json.Unmarshal(body,&payload);err!=nil{t.Fatal(err)}
 for key,b:=range payload.Objects{objects[evidencestore.ObjectLocation{Store:"evidence",Key:key}]=b};_ = json.Unmarshal(enc(payload.Lists["profilingruns"]),&runs);_ = json.Unmarshal(enc(payload.Lists["batchenergyoptimizations"]),&campaigns)
 builder:=fake.NewClientBuilder().WithScheme(catalog.store.client.Scheme());for i:=range runs.Items{builder=builder.WithObjects(&runs.Items[i])};for i:=range campaigns.Items{builder=builder.WithObjects(&campaigns.Items[i])};next,err:=NewCatalog(&Store{client:builder.Build()},objects,1<<20);if err!=nil{t.Fatal(err)};after,err:=next.Refresh(ctx,[]string{source.Namespace});if err!=nil||len(after.Profiles)!=1||len(after.Insufficiencies)!=0{t.Fatalf("converted catalog: %+v %v",after,err)}
 if before.Profiles[0].ID!=after.Profiles[0].ID||!reflect.DeepEqual(before.Profiles[0].Batches,after.Profiles[0].Batches){t.Fatal("profile IDs, batch duration samples, energy or power changed")}
 if !strings.Contains(campaigns.Items[0].Status.Result.Key,"shared-cpu"){t.Fatal("BEO status still references old result")}
}`
