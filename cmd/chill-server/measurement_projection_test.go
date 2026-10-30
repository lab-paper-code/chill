package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMeasurementProjectionNativeListsDetailsAndNamespace(t *testing.T) {
	ctx := context.Background()
	var objects []client.Object
	for _, namespace := range []string{"allowed", "outside"} {
		for _, name := range []string{"z-last", "a-first"} {
			metadata := meta.ObjectMeta{Namespace: namespace, Name: name, UID: "uid-" + "original"}
			objects = append(objects,
				&api.OperationCostMeasurement{ObjectMeta: metadata},
				&api.DispatchOccupancyMeasurement{ObjectMeta: metadata},
			)
		}
	}
	service := &measurementService{
		client: fake.NewClientBuilder().
			WithScheme(measurementScheme(t)).
			WithObjects(objects...).
			Build(),
		opts: options{servingNamespace: "allowed", storeName: "evidence"},
	}
	for _, tc := range []struct {
		name string
		list func(context.Context, string) (httpapi.MeasurementProjectionList, error)
		get  func(context.Context, string, string) (httpapi.MeasurementProjection, error)
	}{
		{"operation", service.listOperationCosts, service.getOperationCost},
		{"occupancy", service.listDispatchOccupancies, service.getDispatchOccupancy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := tc.list(ctx, "allowed")
			if err != nil || len(list.Items) != 2 ||
				list.Items[0].Metadata.Name != "a-first" || list.Items[1].Metadata.Name != "z-last" {
				t.Fatalf("list=%+v err=%v", list, err)
			}
			detail, err := tc.get(ctx, "allowed", "a-first")
			if err != nil || detail.Metadata.UID != "uid-original" ||
				detail.References.CR.Namespace != "allowed" {
				t.Fatalf("detail=%+v err=%v", detail, err)
			}
			if string(list.Items[0].Assessment) != string(detail.Assessment) || len(detail.Assessment) == 0 {
				t.Fatal("list/detail assessment diverged or was omitted")
			}
			if _, err = tc.get(ctx, "allowed", "absent"); !apierrors.IsNotFound(err) {
				t.Fatalf("expected native not found: %v", err)
			}
			if _, err = tc.list(ctx, "outside"); !errors.Is(err, servingkube.ErrForbidden) {
				t.Fatalf("outside list: %v", err)
			}
			if _, err = tc.get(ctx, "outside", "a-first"); !errors.Is(
				err,
				servingkube.ErrForbidden,
			) {
				t.Fatalf("outside get: %v", err)
			}
			service.opts.profileNamespaces = []string{"empty"}
			empty, err := tc.list(ctx, "empty")
			if err != nil || empty.Items == nil || len(empty.Items) != 0 {
				t.Fatalf("empty=%+v err=%v", empty, err)
			}
			service.opts.profileNamespaces = nil
		})
	}
}

type operationProjectionStore struct {
	mu      sync.Mutex
	open    func(context.Context, evidencestore.ObjectLocation) (io.ReadCloser, error)
	payload []byte
	err     error
	reads   []evidencestore.ObjectLocation
}

func (s *operationProjectionStore) OpenLocation(
	ctx context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	s.mu.Lock()
	s.reads = append(s.reads, location)
	s.mu.Unlock()
	if s.open != nil {
		return s.open(ctx, location)
	}
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(bytes.NewReader(s.payload)), nil
}

func (*operationProjectionStore) Delete(context.Context, evidencestore.ObjectLocation) error {
	return errors.New("unexpected deletion during projection")
}

func operationProjectionService(t *testing.T) (*measurementService, *operationProjectionStore) {
	t.Helper()
	operation := servingkube.Operation{
		ID:   "cost-uid.0.prepare",
		Kind: "prepare",
		Node: serving.NodeReference{Name: "node", UID: "node-uid"},
	}
	key := servingkube.OperationMeasurementKey(operation)
	cr := &api.OperationCostMeasurement{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "cost", UID: "cost-uid"},
		Spec: api.OperationCostMeasurementSpec{Node: "node", Steps: []api.OperationCostStep{
			{Name: "prepare", Kind: api.OperationCostStepPrepare},
			{Name: "never-executed", Kind: api.OperationCostStepRemove},
		}},
		Status: api.OperationCostMeasurementStatus{
			Phase:  api.MeasurementPhaseFailed,
			Reason: "original-sequence-reason",
			Steps: []api.OperationCostStepStatus{
				{Name: "prepare", Key: key, Verdict: measurementPhaseFailed},
			},
		},
	}
	start := time.Date(2026, 10, 30, 12, 0, 0, 123, time.UTC)
	record := servingkube.OperationMeasurement{
		SchemaVersion: servingkube.OperationMeasurementSchema,
		Verdict:       measurementPhaseFailed, Reason: "original-step-reason",
		Request: servingkube.MeasurementRequest{Operation: operation, Store: "evidence"},
		Result: servingkube.OperationResult{
			Operation: operation,
			StartedAt: start,
			EndedAt:   start.Add(123456789 * time.Nanosecond),
			Status:    "failed",
		},
		Power: metricobserver.Result{
			Source: metricobserver.SourceIdentity{Endpoint: "private-meter-endpoint"},
		},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	storage := &operationProjectionStore{payload: raw}
	return &measurementService{
		client:  fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(cr).Build(),
		objects: storage,
		opts: options{
			servingNamespace: "allowed",
			storeName:        "evidence",
			maxEvidenceBytes: 1 << 20,
		},
	}, storage
}

func TestMeasurementOperationProjectionLoadsExactEvidenceForListAndDetail(t *testing.T) {
	service, storage := operationProjectionService(t)
	list, err := service.listOperationCosts(context.Background(), "allowed")
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	detail, err := service.getOperationCost(context.Background(), "allowed", "cost")
	if err != nil {
		t.Fatal(err)
	}
	for _, projection := range []httpapi.MeasurementProjection{list.Items[0], detail} {
		raw, err := json.Marshal(projection)
		if err != nil {
			t.Fatal(err)
		}
		var public struct {
			Status struct {
				Phase  string `json:"phase"`
				Reason string `json:"reason"`
				Steps  []struct {
					OperationID   string   `json:"operationID"`
					DurationNanos *int64   `json:"durationNanos"`
					WallJoules    *float64 `json:"wallJoules"`
					Reason        string   `json:"reason"`
					Verdict       string   `json:"verdict"`
				} `json:"steps"`
			} `json:"status"`
		}
		if err := json.Unmarshal(raw, &public); err != nil {
			t.Fatal(err)
		}
		if public.Status.Phase != measurementPhaseFailed ||
			public.Status.Reason != "original-sequence-reason" ||
			len(public.Status.Steps) != 1 {
			t.Fatalf("changed sequence history: %s", raw)
		}
		step := public.Status.Steps[0]
		if step.OperationID != "cost-uid.0.prepare" || step.DurationNanos == nil || *step.DurationNanos != 123456789 ||
			step.WallJoules != nil || step.Verdict != measurementPhaseFailed ||
			step.Reason != "original-step-reason" {
			t.Fatalf("changed step history: %s", raw)
		}
		for _, forbidden := range []string{
			"private-meter-endpoint", "sampling", "stateObservations", `"power"`, `"request"`,
		} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("private evidence leaked: %s", raw)
			}
		}
		if len(projection.References.Evidence) != 1 ||
			projection.References.Evidence[0].Key != storage.reads[0].Key {
			t.Fatalf("changed evidence location: %+v", projection.References)
		}
	}
	if len(storage.reads) != 2 || storage.reads[0] != storage.reads[1] ||
		storage.reads[0].Store != "evidence" {
		t.Fatalf("not exact-key list/detail reads: %+v", storage.reads)
	}
}

func TestMeasurementOperationProjectionEvidenceErrorsDoNotBecomeNull(t *testing.T) {
	for _, name := range []string{"missing", "permission", "transport", "corrupt", "bound"} {
		t.Run(name, func(t *testing.T) {
			service, storage := operationProjectionService(t)
			switch name {
			case "missing":
				storage.err = evidencestore.ErrNotFound
			case "permission":
				storage.err = servingkube.ErrForbidden
			case "transport":
				storage.err = errors.New("store transport failed")
			case "corrupt":
				storage.payload = []byte("{")
			case "bound":
				service.opts.maxEvidenceBytes = 1
			}
			_, listErr := service.listOperationCosts(context.Background(), "allowed")
			_, getErr := service.getOperationCost(context.Background(), "allowed", "cost")
			if listErr == nil || getErr == nil {
				t.Fatalf("referenced evidence error became null: list=%v get=%v", listErr, getErr)
			}
			if storage.err != nil &&
				(!errors.Is(listErr, storage.err) || !errors.Is(getErr, storage.err)) {
				t.Fatalf("lost store error: list=%v get=%v", listErr, getErr)
			}
		})
	}
}

func TestMeasurementOperationProjectionParallelReadsPreserveOrder(t *testing.T) {
	service, storage := operationProjectionService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var source api.OperationCostMeasurement
	if err := service.client.Get(ctx, types.NamespacedName{Namespace: "allowed", Name: "cost"}, &source); err != nil {
		t.Fatal(err)
	}
	var record servingkube.OperationMeasurement
	if err := json.Unmarshal(storage.payload, &record); err != nil {
		t.Fatal(err)
	}
	const count = 10
	objects := make([]client.Object, 0, count)
	payloads := make(map[string][]byte, count)
	firstKey, lastKey := "", ""
	for i := range count {
		cr := source.DeepCopy()
		cr.Name = fmt.Sprintf("measurement-%02d", i)
		cr.UID = types.UID(cr.Name)
		cr.ResourceVersion = ""
		operation := record.Request.Operation
		operation.ID = cr.Name + ".0.prepare"
		record.Request.Operation, record.Result.Operation = operation, operation
		key := servingkube.OperationMeasurementKey(operation)
		cr.Status.Steps[0].Key = key
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		payloads[key] = payload
		objects = append(objects, cr)
		if i == 0 {
			firstKey = key
		}
		lastKey = key
	}
	service.client = fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(objects...).Build()
	releaseFirst := make(chan struct{})
	firstStarted := make(chan struct{})
	var active, maximum atomic.Int32
	storage.open = func(ctx context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
		running := active.Add(1)
		defer active.Add(-1)
		for current := maximum.Load(); running > current; current = maximum.Load() {
			if maximum.CompareAndSwap(current, running) {
				break
			}
		}
		if location.Key == firstKey {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		} else {
			select {
			case <-firstStarted:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if location.Key == lastKey {
			close(releaseFirst)
		}
		return io.NopCloser(bytes.NewReader(payloads[location.Key])), nil
	}
	list, err := service.listOperationCosts(ctx, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != count || len(storage.reads) != count || maximum.Load() < 2 || maximum.Load() > 8 {
		t.Fatalf("unexpected list/read/concurrency counts: %d/%d/%d", len(list.Items), len(storage.reads), maximum.Load())
	}
	for i, item := range list.Items {
		if want := fmt.Sprintf("measurement-%02d", i); item.Metadata.Name != want {
			t.Fatalf("completion order leaked into list index %d: got %s, want %s", i, item.Metadata.Name, want)
		}
	}
}
