package measurement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	batch "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	occupancyQueuedTest        = "queued"
	occupancyDeletedTest       = "deleted"
	occupancyLegacyDeletedTest = "legacy deleted"
)

func occupancyTestService(t *testing.T) (*Service, occupancyFixture, *int) {
	t.Helper()
	f := syntheticOccupancyFixture()
	raw, err := os.ReadFile("../../../config/samples/serving-scheduler.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["dispatchOccupancy"], err = json.Marshal(f.policy)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	f.cm.Data = map[string]string{"scheduler.json": string(raw)}
	beo := &api.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Name: "beo", Namespace: "allowed", UID: "beo-uid"}}
	beo.Spec.State.TargetNode = "node"
	beo.Status.Phase = string(api.MeasurementPhaseComplete)
	creates := new(int)
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(beo, f.workload, f.preprocessor).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(
			ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption,
		) error {
			*creates++
			return c.Create(ctx, obj, opts...)
		}}).
		Build()
	scheduler := &SchedulerInputs{Lock: &sync.Mutex{}, GatewayRequestTimeout: 2 * time.Second, DecodePolicy: decodeTestPolicy,
		LoadProfiles: func() (string, []serving.Profile, map[string]servingkube.PrepareSpec, bool) {
			return f.cm.ResourceVersion, []serving.Profile{*f.profile}, map[string]servingkube.PrepareSpec{f.profile.ID: f.preparation}, true
		},
	}
	service := &Service{
		client:    native,
		kube:      kubefake.NewSimpleClientset(f.cm, f.node),
		scheduler: scheduler,
		opts: Config{
			ServingNamespace:   "serving",
			ProfileNamespaces:  []string{"allowed"},
			SchedulerConfigMap: "scheduler",
			SchedulerConfigKey: "scheduler.json",
			MaxRequestBytes:    1 << 20,
		},
	}
	return service, f, creates
}

func TestOccupancyServiceDryRunSameFrozenObjectWithoutCreate(t *testing.T) {
	service, f, creates := occupancyTestService(t)
	task, err := service.BuildOccupancy(context.Background(), "allowed", f.intent)
	if err != nil {
		t.Fatal(err)
	}
	dry, err := service.SubmitOccupancy(context.Background(), *task, true)
	if err != nil || *creates != 0 || dry.Request != measurementRequestNotSubmitted ||
		!reflect.DeepEqual(dry.Task, task) {
		t.Fatal("dry run mutated or changed the frozen object", err)
	}
	real, err := service.SubmitOccupancy(context.Background(), *task, false)
	if err != nil || *creates != 1 || real.Request != measurementRequestAccepted ||
		!reflect.DeepEqual(real.Task.Spec, dry.Task.Spec) {
		t.Fatal("create changed frozen protocol", err)
	}
	stored := &api.DispatchOccupancyMeasurement{}
	if err = service.client.Get(context.Background(), client.ObjectKeyFromObject(task), stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Spec, task.Spec) {
		t.Fatal("native spec differs")
	}
	_, err = service.BuildOccupancy(context.Background(), "outside", f.intent)
	if !errors.Is(err, servingkube.ErrForbidden) {
		t.Fatal("namespace escaped", err)
	}
}

func TestOccupancyServicePolicyFailsClosed(t *testing.T) {
	for _, mode := range []string{
		"missing block", "invalid block", "unknown field", "unaccepted revision", "missing ConfigMap", "missing scheduler",
		"missing allowance",
	} {
		t.Run(mode, func(t *testing.T) {
			service, f, _ := occupancyTestService(t)
			switch mode {
			case "missing scheduler":
				service.scheduler = nil
			case "missing ConfigMap":
				err := service.kube.CoreV1().ConfigMaps("serving").Delete(
					context.Background(), "scheduler", meta.DeleteOptions{})
				if err != nil {
					t.Fatal(err)
				}
			default:
				cm, err := service.kube.CoreV1().
					ConfigMaps("serving").
					Get(context.Background(), "scheduler", meta.GetOptions{})
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
					wire["dispatchOccupancy"].(map[string]any)["minimumPairs"] = 0
				case "missing allowance":
					delete(wire["dispatchOccupancy"].(map[string]any), "collectionSeconds")
				case "unknown field":
					wire["dispatchOccupancy"].(map[string]any)["refillSeconds"] = .02
				case "unaccepted revision":
					cm.ResourceVersion = "new"
				}
				raw, _ := json.Marshal(wire)
				cm.Data["scheduler.json"] = string(raw)
				_, err = service.kube.CoreV1().ConfigMaps("serving").Update(
					context.Background(), cm, meta.UpdateOptions{})
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := service.BuildOccupancy(context.Background(), "allowed", f.intent)
			requireOccupancyError(t, err, "OccupancyPolicyUnavailable")
		})
	}
}

func TestOccupancyDeleteFencesAndOnlyRemovesOwnedEvidence(t *testing.T) {
	for _, mode := range []string{
		occupancyDeletedTest, "Measuring", "wrong UID", "wrong version", "foreign location", "foreign record",
		"shared evidence", "active load", "evidence failure", "native failure",
		occupancyQueuedTest, occupancyLegacyDeletedTest,
	} {
		t.Run(mode, func(t *testing.T) {
			f := syntheticOccupancyFixture()
			task, err := f.build()
			if err != nil {
				t.Fatal(err)
			}
			task.UID = "dom-uid"
			task.ResourceVersion = "7"
			task.Status.Phase = api.MeasurementPhaseComplete
			prefix, schemaVersion := servingkube.BurstOccupancyPrefix, servingkube.BurstOccupancySchema
			if mode == occupancyLegacyDeletedTest {
				task.Spec.MinimumPairs = 0
				prefix, schemaVersion = servingkube.DispatchOccupancyPrefix, servingkube.DispatchOccupancySchema
			}
			own := prefix + "node-uid/dom-uid.json"
			task.Status.Result = &api.ProfilingRunEvidenceReference{
				Store:         "evidence",
				Key:           own,
				SchemaVersion: schemaVersion,
			}
			record := servingkube.BurstOccupancyRecord{
				SchemaVersion: schemaVersion,
				Node:          serving.NodeReference{Name: "node", UID: "node-uid"},
				ProfileID:     f.profile.ID,
				Conditions:    task.Spec,
			}
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
				record.Conditions.Workload = occupancyForeignSelectionTest
			case occupancyQueuedTest:
				task.Status.Phase = ""
				task.Status.Result = nil
			}
			raw, _ := json.Marshal(record)
			if mode == occupancyLegacyDeletedTest {
				raw, _ = json.Marshal(servingkube.DispatchOccupancyRecord{
					SchemaVersion: schemaVersion, Node: record.Node, ProfileID: record.ProfileID, Conditions: task.Spec,
				})
			}
			foreign := prefix + "node-uid/foreign.json"
			storage := deletionStore(t, map[string][]byte{own: raw, foreign: []byte(`{"unrelated":true}`)})
			if mode == occupancyQueuedTest {
				storage.mu.Lock()
				delete(storage.objects, own)
				storage.mu.Unlock()
			}
			if mode == "evidence failure" {
				storage.failKey = own
			}
			deletes := 0
			native := occupancyDeletionClient(t, task, mode, &deletes)
			kube := kubefake.NewSimpleClientset(f.node)
			if mode == "active load" {
				sum := sha256.Sum256([]byte(task.UID))
				job := &batch.Job{
					ObjectMeta: meta.ObjectMeta{
						Namespace: "serving",
						Name:      "occupancy-" + hex.EncodeToString(sum[:10]),
					},
					Status: batch.JobStatus{Active: 1},
				}
				if _, err := kube.BatchV1().Jobs("serving").Create(context.Background(), job, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			service := &Service{
				client:  native,
				kube:    kube,
				objects: storage.store,
				opts: Config{
					ServingNamespace:  "serving",
					ProfileNamespaces: []string{"allowed"},
					StoreName:         "evidence",
					MaxEvidenceBytes:  1 << 20,
				},
			}
			result, err := service.DeleteOccupancy(context.Background(), "allowed", task.Name, uid, version)
			success := mode == occupancyDeletedTest || mode == occupancyQueuedTest || mode == occupancyLegacyDeletedTest
			if (err == nil) != success {
				t.Fatalf("delete success=%v got %v", success, err)
			}
			if !storage.contains(foreign) {
				t.Fatal("foreign evidence removed")
			}
			if success {
				if deletes != 1 || result.Request != measurementRequestDeletionRequested || !result.EvidenceDeleted {
					t.Fatal("missing native delete receipt")
				}
				err := native.Get(context.Background(), client.ObjectKeyFromObject(task), &api.DispatchOccupancyMeasurement{})
				if !apierrors.IsNotFound(err) {
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

func occupancyDeletionClient(
	t *testing.T,
	task *api.DispatchOccupancyMeasurement,
	mode string,
	deletes *int,
) client.Client {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(task)
	if mode == "shared evidence" {
		other := task.DeepCopy()
		other.Name = occupancyForeignSelectionTest
		other.UID = "other-uid"
		builder = builder.WithObjects(other)
	}
	return builder.WithInterceptorFuncs(interceptor.Funcs{Delete: func(
		ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption,
	) error {
		*deletes++
		options := &client.DeleteOptions{}
		for _, opt := range opts {
			opt.ApplyToDelete(options)
		}
		if options.Preconditions == nil || *options.Preconditions.UID != task.UID ||
			*options.Preconditions.ResourceVersion != "7" {
			t.Fatal("native preconditions missing")
		}
		if mode == "native failure" {
			return errors.New("delete unavailable")
		}
		return c.Delete(ctx, obj, opts...)
	}}).
		Build()
}

func decodeTestPolicy(raw string, _ int64) (*DispatchOccupancyPolicy, error) {
	var config struct {
		DispatchOccupancy *DispatchOccupancyPolicy `json:"dispatchOccupancy"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, err
	}
	if config.DispatchOccupancy != nil {
		if err := config.DispatchOccupancy.Validate(); err != nil {
			return nil, err
		}
	}
	return config.DispatchOccupancy, nil
}

// OccupancyHTTPFixture shares the real service fixture with the external transport test.
func OccupancyHTTPFixture(t *testing.T) (*Service, *kubefake.Clientset, *int) {
	service, _, creates := occupancyTestService(t)
	return service, service.kube.(*kubefake.Clientset), creates
}
