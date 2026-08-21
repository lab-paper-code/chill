package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
)

const preflightTestNamespace = "chill-system"

func TestExecutePreflightRequiresFile(t *testing.T) {
	calls := 0
	deps := dependencies{newPreflightQuery: func(preflightOptions) (batchSaturationPreflightQuery, error) {
		calls++
		return nil, nil
	}}
	err := execute(context.Background(), []string{"preflight"}, io.Discard, io.Discard, deps)
	if !errors.Is(err, errUsage) || calls != 0 {
		t.Fatalf("execute() error = %v, query constructions = %d", err, calls)
	}
}

func TestExecutePreflightRejectsUnreadableAndUndecodableManifests(t *testing.T) {
	malformed := filepath.Join(t.TempDir(), "malformed.yaml")
	if err := os.WriteFile(malformed, []byte("metadata: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		path  string
		match string
	}{
		{name: "unreadable", path: filepath.Join(t.TempDir(), "missing.yaml"), match: "read BatchSaturation manifest"},
		{name: "undecodable", path: malformed, match: "decode BatchSaturation manifest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := dependencies{newPreflightQuery: newBatchSaturationPreflightQuery}
			err := execute(context.Background(), []string{"preflight", "-f", test.path}, io.Discard, io.Discard, deps)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("execute() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestExecutePreflightWritesResolvingTable(t *testing.T) {
	query := &fakeBatchSaturationPreflightQuery{preflight: testBatchSaturationPreflight(true)}
	deps := dependencies{newPreflightQuery: func(opts preflightOptions) (batchSaturationPreflightQuery, error) {
		if opts.file != "campaign.yaml" || opts.namespace != preflightTestNamespace ||
			opts.consoleService != "console" || opts.output != outputFormatTable {
			t.Fatalf("options = %#v", opts)
		}
		return query, nil
	}}
	var stdout strings.Builder
	err := execute(context.Background(), []string{
		"preflight", "-f", "campaign.yaml", "-n", preflightTestNamespace, "--console-service", "console",
	}, &stdout, io.Discard, deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"READ_AT", "NAMESPACE", preflightTestNamespace, "CAMPAIGN", "hourly-curve", "RESOLVES", "yes",
		"PLANNED_BATCH_SIZES", "1,2,4", "mobilenet.rknn", "rknnlite", "orangepi-5-plus", "fixed",
		"float32", "224,224,3",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("table missing %q:\n%s", want, stdout.String())
		}
	}
	if query.calls != 1 {
		t.Fatalf("query calls = %d", query.calls)
	}
}

func TestExecutePreflightWritesNonResolvingReason(t *testing.T) {
	preflight := testBatchSaturationPreflight(false)
	query := &fakeBatchSaturationPreflightQuery{preflight: preflight}
	deps := dependencies{newPreflightQuery: func(preflightOptions) (batchSaturationPreflightQuery, error) {
		return query, nil
	}}
	var stdout strings.Builder
	if err := execute(context.Background(), []string{
		"preflight", "-f", "campaign.yaml", "-n", preflightTestNamespace,
	}, &stdout, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "RESOLVES") || !strings.Contains(stdout.String(), "no") ||
		!strings.Contains(stdout.String(), preflight.Reason) {
		t.Fatalf("table =\n%s", stdout.String())
	}
}

func TestExecutePreflightWritesJSONRoundTrip(t *testing.T) {
	want := testBatchSaturationPreflight(true)
	deps := dependencies{newPreflightQuery: func(preflightOptions) (batchSaturationPreflightQuery, error) {
		return &fakeBatchSaturationPreflightQuery{preflight: want}, nil
	}}
	var stdout strings.Builder
	if err := execute(context.Background(), []string{
		"preflight", "-f", "campaign.yaml", "-o", "json",
	}, &stdout, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	expectedJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var expected, got any
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("output = %#v, want %#v", got, expected)
	}
}

type fakeBatchSaturationPreflightQuery struct {
	preflight operatorconsole.BatchSaturationPreflight
	err       error
	calls     int
}

func (q *fakeBatchSaturationPreflightQuery) Read(
	context.Context,
) (operatorconsole.BatchSaturationPreflight, error) {
	q.calls++
	return q.preflight, q.err
}

func testBatchSaturationPreflight(resolves bool) operatorconsole.BatchSaturationPreflight {
	preflight := operatorconsole.BatchSaturationPreflight{
		APIVersion: operatorconsole.BatchSaturationPreflightAPIVersion,
		Kind:       "BatchSaturationPreflight",
		ReadAt:     time.Date(2026, 8, 19, 1, 2, 3, 4, time.UTC),
		Campaign: operatorconsole.BatchSaturationPreflightCampaign{
			Namespace: preflightTestNamespace,
			Name:      "hourly-curve",
		},
		Resolves: resolves,
	}
	if !resolves {
		preflight.Reason = "referenced DeviceClass does not exist"
		return preflight
	}
	preflight.Plan = json.RawMessage(`{
		"schemaVersion":"chill.dacs.io/profile-curve-plan.v1alpha1",
		"shared":{
			"plannedBatchSizes":[1,2,4],
			"artifact":{"name":"mobilenet.rknn","executionPath":"rknnlite"},
			"deviceClass":"orangepi-5-plus",
			"powerMode":"fixed",
			"tensor":{"dataType":"float32","inputSampleShape":[224,224,3]}
		}
	}`)
	return preflight
}
