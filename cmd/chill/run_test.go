package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestExecuteInventoryWritesTable(t *testing.T) {
	query := &fakeInventoryQuery{snapshot: testInventorySnapshot()}
	deps := dependencies{newInventoryQuery: func(opts inventoryOptions) (inventoryQuery, error) {
		if opts.kubeconfig != "/tmp/config" || opts.context != "edge" || opts.output != "table" {
			t.Fatalf("options = %#v", opts)
		}
		return query, nil
	}}
	var stdout strings.Builder

	err := execute(context.Background(), []string{
		"inventory", "--kubeconfig", "/tmp/config", "--context", "edge",
	}, &stdout, io.Discard, deps)
	if err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	for _, want := range []string{
		"READ_AT", "classes-rv", "CLASS", "orin", "arm64", "8Gi", "15W", "orin-1", "True", "true",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("table missing %q:\n%s", want, stdout.String())
		}
	}
	if query.calls != 1 {
		t.Fatalf("query calls = %d", query.calls)
	}
}

func TestExecuteInventoryWritesStructuredJSON(t *testing.T) {
	deps := dependencies{newInventoryQuery: func(inventoryOptions) (inventoryQuery, error) {
		return &fakeInventoryQuery{snapshot: testInventorySnapshot()}, nil
	}}
	var stdout strings.Builder
	if err := execute(context.Background(), []string{"inventory", "-o", "json"}, &stdout, io.Discard, deps); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	var document inventoryDocument
	if err := json.Unmarshal([]byte(stdout.String()), &document); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if document.DeviceClassesResourceVersion != "classes-rv" || len(document.Classes) != 1 ||
		document.Classes[0].UID != "class-uid" || document.Classes[0].NodeSelector["edge.dacs.io/device-class"] != "orin" ||
		len(document.Classes[0].PowerModes) != 1 || document.Classes[0].PowerModes[0].NvpID == nil ||
		*document.Classes[0].PowerModes[0].NvpID != 0 || len(document.Classes[0].Nodes) != 1 ||
		document.Classes[0].Nodes[0].Ready != "True" || !document.Classes[0].Nodes[0].SchedulingDisabled {
		t.Fatalf("document = %#v", document)
	}
}

func TestExecuteRejectsInvalidUsageBeforeConstructingQuery(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"inventory", "extra"}, {"inventory", "-o", "yaml"}} {
		calls := 0
		deps := dependencies{newInventoryQuery: func(inventoryOptions) (inventoryQuery, error) {
			calls++
			return nil, nil
		}}
		err := execute(context.Background(), args, io.Discard, io.Discard, deps)
		if !errors.Is(err, errUsage) || calls != 0 {
			t.Fatalf("execute(%v) error = %v, calls = %d", args, err, calls)
		}
	}
}

func TestExecuteHelpDoesNotConstructQuery(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"inventory", "--help"}} {
		calls := 0
		deps := dependencies{newInventoryQuery: func(inventoryOptions) (inventoryQuery, error) {
			calls++
			return nil, nil
		}}
		var output strings.Builder
		if err := execute(context.Background(), args, &output, &output, deps); err != nil {
			t.Fatalf("execute(%v) error = %v", args, err)
		}
		if output.Len() == 0 || calls != 0 {
			t.Fatalf("execute(%v) output = %q, calls = %d", args, output.String(), calls)
		}
	}
}

func TestExecutePreservesQueryFailure(t *testing.T) {
	want := errors.New("query failed")
	deps := dependencies{newInventoryQuery: func(inventoryOptions) (inventoryQuery, error) {
		return &fakeInventoryQuery{err: want}, nil
	}}
	err := execute(context.Background(), []string{"inventory"}, io.Discard, io.Discard, deps)
	if !errors.Is(err, want) {
		t.Fatalf("execute() error = %v", err)
	}
}

type fakeInventoryQuery struct {
	snapshot operatorconsole.InventorySnapshot
	err      error
	calls    int
}

func (q *fakeInventoryQuery) Read(context.Context) (operatorconsole.InventorySnapshot, error) {
	q.calls++
	return q.snapshot, q.err
}

func testInventorySnapshot() operatorconsole.InventorySnapshot {
	nvpID := 0
	return operatorconsole.InventorySnapshot{
		ReadAt:                       time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
		DeviceClassesResourceVersion: "classes-rv",
		NodesResourceVersion:         "nodes-rv",
		Classes: []deviceclass.InventoryClass{{
			Name: "orin", UID: "class-uid", ResourceVersion: "class-rv",
			Spec: edgev1alpha1.DeviceClassSpec{
				NodeSelector: map[string]string{"edge.dacs.io/device-class": "orin"},
				Architecture: "arm64", MemoryBytes: resource.MustParse("8Gi"),
				Accelerator: "jetson-orin", PowerModes: []edgev1alpha1.PowerMode{{Name: "15W", NvpID: &nvpID}},
			},
			Nodes: []deviceclass.InventoryNode{{
				Name: "orin-1", UID: "node-uid", ResourceVersion: "node-rv",
				Ready: corev1.ConditionTrue, SchedulingDisabled: true,
			}},
		}},
	}
}
