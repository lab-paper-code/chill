package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sizingInput = `{"batch":2,"maxQueueRequests":64,"maxQueueBytes":2048,"framedDecodedRequestUpperBytes":256,` +
	`"requestLifetimeSeconds":2.25,"deliveryMarginMilliseconds":250,"maxBatchServiceSeconds":0.125}`

func TestSizeSaturationCLIWithoutConfigurationOrServer(t *testing.T) {
	t.Setenv("NODE_NAME", "")
	input, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	previousInput, previousOutput := os.Stdin, os.Stdout
	t.Cleanup(func() {
		os.Stdin, os.Stdout = previousInput, previousOutput
		_ = input.Close()
		_ = output.Close()
	})
	if _, err := input.WriteString(sizingInput); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	os.Stdin, os.Stdout = input, output
	flags := flag.NewFlagSet("serving-worker", flag.ContinueOnError)
	var opts options
	opts.bindFlags(flags)
	if err := flags.Parse([]string{
		"--size-saturation", "--config=" + filepath.Join(t.TempDir(), "missing.json"),
		"--bind-address=not-a-listener-address",
	}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), opts); err != nil {
		t.Fatalf("sizing must bypass config, node validation and server startup: %v", err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	decoder := json.NewDecoder(output)
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"batch": float64(2), "maxBatchServiceSeconds": 0.125, "serviceTimeSource": "run.sparse.maxObservedSeconds",
		"requestLifetimeSeconds": 2.25, "marginSeconds": 0.25, "workerCap": float64(64), "queueBytesCap": float64(2048),
		"framedDecodedRequestUpperBytes": float64(256), "workers": float64(8), "limitingBound": "bytes-cap",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CLI must retain complete observed-tail sizing provenance and bound tie order: got %#v want %#v", got, want)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("CLI stdout must contain exactly one result: %v", err)
	}
}

func TestSizeSaturationRejectsInvalidInputsWithoutOutput(t *testing.T) {
	for _, input := range []string{
		`{`,
		strings.Replace(sizingInput, `"batch":2`, `"batch":2,"unknown":1`, 1),
		sizingInput + `{}`,
		strings.Replace(sizingInput, `"maxBatchServiceSeconds":0.125`, `"maxBatchServiceSeconds":0`, 1),
		strings.Replace(sizingInput, `"maxQueueRequests":64`, `"maxQueueRequests":1`, 1),
	} {
		var output bytes.Buffer
		if err := sizeSaturation(strings.NewReader(input), &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid sizing input must fail without a result: input=%s output=%s err=%v", input, output.String(), err)
		}
	}
}
