package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	batch "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func occupancyTestService(t *testing.T) (*measurementService, occupancyFixture, *int) {
	t.Helper()
	f := syntheticOccupancyFixture()
	raw, err := os.ReadFile("../../config/samples/serving-scheduler.json")
	if err != nil {
		t.Fatal(err)
	}
	config, err := decodeSchedulerConfiguration(string(raw), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	config.DispatchOccupancy = &f.policy
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	f.cm.Data = map[string]string{"scheduler.json": string(raw)}
	beo := &api.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Name: "beo", Namespace: "allowed", UID: "beo-uid"}}
	beo.Spec.State.TargetNode = "node"
	beo.Status.Phase = "Complete"
	creates := new(int)
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(beo, f.workload, f.preprocessor).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			*creates++
			return c.Create(ctx, obj, opts...)
		}}).Build()
	scheduler := &schedulerService{gatewayRequestTimeout: 2 * time.Second}
	scheduler.profiles.Store(&httpapi.ProfilesView{ConfigVersion: f.cm.ResourceVersion, CatalogView: servingkube.CatalogView{Profiles: []serving.Profile{*f.profile}},
		Preparations: map[string]servingkube.PrepareSpec{f.profile.ID: f.preparation}})
	service := &measurementService{client: native, kube: kubefake.NewSimpleClientset(f.cm, f.node), scheduler: scheduler,
		opts: options{servingNamespace: "serving", profileNamespaces: []string{"allowed"}, schedulerConfigMap: "scheduler", schedulerConfigKey: "scheduler.json", maxRequestBytes: 1 << 20}}
	return service, f, creates
}

func TestOccupancyServiceDryRunSameFrozenObjectWithoutCreate(t *testing.T) {
	service, f, creates := occupancyTestService(t)
	task, err := service.buildOccupancy(context.Background(), "allowed", f.intent)
	if err != nil {
		t.Fatal(err)
	}
	dry, err := service.submitOccupancy(context.Background(), *task, true)
	if err != nil || *creates != 0 || dry.Request != "NOT_SUBMITTED" || !reflect.DeepEqual(dry.Task, task) {
		t.Fatal("dry run mutated or changed the frozen object", err)
	}
	real, err := service.submitOccupancy(context.Background(), *task, false)
	if err != nil || *creates != 1 || real.Request != "Accepted" || !reflect.DeepEqual(real.Task.Spec, dry.Task.Spec) {
		t.Fatal("create changed frozen protocol", err)
	}
	stored := &api.DispatchOccupancyMeasurement{}
	if err = service.client.Get(context.Background(), client.ObjectKeyFromObject(task), stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Spec, task.Spec) {
		t.Fatal("native spec differs")
	}
	_, err = service.buildOccupancy(context.Background(), "outside", f.intent)
	if !errors.Is(err, servingkube.ErrForbidden) {
		t.Fatal("namespace escaped", err)
	}
}

func TestOccupancyServicePolicyFailsClosed(t *testing.T) {
	for _, mode := range []string{"missing block", "invalid block", "unknown field", "unaccepted revision", "missing ConfigMap", "missing scheduler"} {
		t.Run(mode, func(t *testing.T) {
			service, f, _ := occupancyTestService(t)
			switch mode {
			case "missing scheduler":
				service.scheduler = nil
			case "missing ConfigMap":
				if err := service.kube.CoreV1().ConfigMaps("serving").Delete(context.Background(), "scheduler", meta.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			default:
				cm, err := service.kube.CoreV1().ConfigMaps("serving").Get(context.Background(), "scheduler", meta.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]any
				if err = json.Unmarshal([]byte(cm.Data["scheduler.json"]), &wire); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "missing block":
					delete(wire, "dispatchOccupancy")
				case "invalid block":
					wire["dispatchOccupancy"].(map[string]any)["minimumBatches"] = 1
				case "unknown field":
					wire["dispatchOccupancy"].(map[string]any)["workers"] = 6
				case "unaccepted revision":
					cm.ResourceVersion = "new"
				}
				raw, _ := json.Marshal(wire)
				cm.Data["scheduler.json"] = string(raw)
				if _, err = service.kube.CoreV1().ConfigMaps("serving").Update(context.Background(), cm, meta.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.buildOccupancy(context.Background(), "allowed", f.intent)
			requireOccupancyError(t, err, "OccupancyPolicyUnavailable")
		})
	}
}

func TestOccupancyDeleteFencesAndOnlyRemovesOwnedEvidence(t *testing.T) {
	for _, mode := range []string{"deleted", "Measuring", "wrong UID", "wrong version", "foreign location", "foreign record", "shared evidence", "active load", "evidence failure", "native failure", "queued"} {
		t.Run(mode, func(t *testing.T) {
			f := syntheticOccupancyFixture()
			task, err := f.build()
			if err != nil {
				t.Fatal(err)
			}
			task.UID = "dom-uid"
			task.ResourceVersion = "7"
			task.Status.Phase = api.MeasurementPhaseComplete
			own := servingkube.DispatchOccupancyPrefix + "node-uid/dom-uid.json"
			task.Status.Result = &api.ProfilingRunEvidenceReference{Store: "evidence", Key: own, SchemaVersion: servingkube.DispatchOccupancySchema}
			record := servingkube.DispatchOccupancyRecord{SchemaVersion: servingkube.DispatchOccupancySchema, Node: serving.NodeReference{Name: "node", UID: "node-uid"}, ProfileID: f.profile.ID, Conditions: task.Spec}
			uid, version := string(task.UID), task.ResourceVersion
			switch mode {
			case "Measuring":
				task.Status.Phase = api.MeasurementPhaseMeasuring
			case "wrong UID":
				uid = "replacement"
			case "wrong version":
				version = "6"
			case "foreign location":
				task.Status.Result.Key = "batch-energy-optimizations/foreign/result.json"
			case "foreign record":
				record.Conditions.Workload = "other"
			case "queued":
				task.Status.Phase = ""
				task.Status.Result = nil
			}
			raw, _ := json.Marshal(record)
			foreign := "serving/dispatch-occupancy/node-uid/foreign.json"
			storage := deletionStore(t, map[string][]byte{own: raw, foreign: []byte(`{"unrelated":true}`)})
			if mode == "queued" {
				storage.mu.Lock()
				delete(storage.objects, own)
				storage.mu.Unlock()
			}
			if mode == "evidence failure" {
				storage.failKey = own
			}
			builder := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task)
			if mode == "shared evidence" {
				other := task.DeepCopy()
				other.Name = "other"
				other.UID = "other-uid"
				builder = builder.WithObjects(other)
			}
			deletes := 0
			native := builder.WithInterceptorFuncs(interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deletes++
				options := &client.DeleteOptions{}
				for _, opt := range opts {
					opt.ApplyToDelete(options)
				}
				if options.Preconditions == nil || *options.Preconditions.UID != task.UID || *options.Preconditions.ResourceVersion != "7" {
					t.Fatal("native preconditions missing")
				}
				if mode == "native failure" {
					return errors.New("delete unavailable")
				}
				return c.Delete(ctx, obj, opts...)
			}}).Build()
			kube := kubefake.NewSimpleClientset(f.node)
			if mode == "active load" {
				sum := sha256.Sum256([]byte(task.UID))
				job := &batch.Job{ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "occupancy-" + hex.EncodeToString(sum[:10])}, Status: batch.JobStatus{Active: 1}}
				if _, err := kube.BatchV1().Jobs("serving").Create(context.Background(), job, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			service := &measurementService{client: native, kube: kube, objects: storage.store, opts: options{servingNamespace: "serving", profileNamespaces: []string{"allowed"}, storeName: "evidence", maxEvidenceBytes: 1 << 20}}
			result, err := service.deleteOccupancy(context.Background(), "allowed", task.Name, uid, version)
			success := mode == "deleted" || mode == "queued"
			if (err == nil) != success {
				t.Fatalf("delete success=%v got %v", success, err)
			}
			if !storage.contains(foreign) {
				t.Fatal("foreign evidence removed")
			}
			if success {
				if deletes != 1 || result.Request != "DeletionRequested" || !result.EvidenceDeleted {
					t.Fatal("missing native delete receipt")
				}
				if err := native.Get(context.Background(), client.ObjectKeyFromObject(task), &api.DispatchOccupancyMeasurement{}); !apierrors.IsNotFound(err) {
					t.Fatal("native retained", err)
				}
			} else if mode == "native failure" {
				requireOccupancyError(t, err, "MeasurementDeletionIncomplete")
			} else if deletes != 0 {
				t.Fatal("unsafe native delete")
			}
		})
	}
}
