package firstfloor

// Adopted from teammate firstfloor's persistent length-prefixed IPC runner.
import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"time"
)

// wireTensor describes one BYTES field: shape [n] and the byte length of its n
// framed elements in the payload.
type wireTensor struct {
	Name     string `json:"name"`
	Datatype string `json:"datatype"`
	Shape    []int  `json:"shape"`
	Bytes    int    `json:"bytes"`
}

// itemResult is one request's preprocess report: a unit count or a failure reason.
type itemResult struct {
	Units int    `json:"units,omitempty"`
	Error string `json:"error,omitempty"`
}
type frame struct {
	Type          string           `json:"type"`
	BatchID       uint64           `json:"batch_id,omitempty"`
	BatchSizes    []int            `json:"batch_sizes,omitempty"`
	Inputs        []string         `json:"inputs,omitempty"`
	Outputs       []string         `json:"outputs,omitempty"`
	MaxNewTokens  int              `json:"max_new_tokens"`
	Tensors       []wireTensor     `json:"tensors,omitempty"`
	Items         []itemResult     `json:"items,omitempty"`
	DataBytes     int              `json:"data_bytes"`
	Error         string           `json:"error,omitempty"`
	Metadata      *RuntimeMetadata `json:"metadata,omitempty"`
	ServiceMS     float64          `json:"service_ms,omitempty"`
	PreprocessMS  float64          `json:"preprocess_ms,omitempty"`
	BackendMS     float64          `json:"backend_ms,omitempty"`
	PostprocessMS float64          `json:"postprocess_ms,omitempty"`
	FirstTokenMS  float64          `json:"first_token_ms,omitempty"`
}

// batchError ends one batch whose module result broke the contract; the runner keeps serving.
type batchError struct{ reason string }

func (e *batchError) Error() string { return e.reason }

// preprocessError ends one request whose sample the preprocessor module rejected.
type preprocessError struct{ reason string }

func (e *preprocessError) Error() string { return e.reason }

// unframe splits n elements, each framed as uint32 little-endian length plus bytes.
func unframe(b []byte, n int) ([][]byte, error) {
	out := make([][]byte, 0, n)
	for len(b) > 0 && len(out) < n {
		if len(b) < 4 {
			return nil, errors.New("truncated BYTES element length")
		}
		size := binary.LittleEndian.Uint32(b)
		b = b[4:]
		if uint64(size) > uint64(len(b)) {
			return nil, errors.New("truncated BYTES element")
		}
		out = append(out, b[:size:size])
		b = b[size:]
	}
	if len(out) != n || len(b) != 0 {
		return nil, errors.New("BYTES element count differs from shape")
	}
	return out, nil
}

type Runner struct {
	command    *exec.Cmd
	input      io.WriteCloser
	output     io.ReadCloser
	cancel     context.CancelFunc
	exited     chan struct{}
	waitErr    error
	timeout    time.Duration
	batchSizes []int
	metadata   RuntimeMetadata
	generative bool
	closeOnce  sync.Once
}

func StartRunner(ctx context.Context, cfg *Config) (*Runner, error) {
	processCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(processCtx, cfg.RunnerCommand[0], cfg.RunnerCommand[1:]...)
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, childOutput, err := os.Pipe()
	if err != nil {
		_ = input.Close() // Startup already failed; release the unused pipe.
		cancel()
		return nil, err
	}
	command.Stdout = childOutput
	if err = command.Start(); err != nil {
		_ = input.Close() // Startup already failed; release all unused pipes.
		_ = output.Close()
		_ = childOutput.Close()
		cancel()
		return nil, err
	}
	r := &Runner{command: command, input: input, output: output, cancel: cancel, exited: make(chan struct{}), timeout: time.Duration(cfg.RunnerTimeoutMS) * time.Millisecond, batchSizes: cfg.BatchSizes, generative: cfg.MaxNewTokens > 0}
	go func() { r.waitErr = command.Wait(); close(r.exited) }()
	if err = childOutput.Close(); err != nil {
		r.Close()
		return nil, fmt.Errorf("close parent runner output descriptor: %w", err)
	}
	type reply struct {
		header frame
		err    error
	}
	ready := make(chan reply, 1)
	go func() {
		e := writeFrame(input, frame{Type: "init", Inputs: cfg.Inputs, Outputs: cfg.Outputs, BatchSizes: cfg.BatchSizes, MaxNewTokens: cfg.MaxNewTokens}, nil)
		var h frame
		if e == nil {
			var b []byte
			h, b, e = readFrame(output)
			if e == nil && h.Type == "error" {
				e = fmt.Errorf("runner startup: %s", h.Error)
			}
			if e == nil && (h.Type != "ready" || len(b) != 0 || h.Metadata == nil) {
				e = errors.New("runner did not send ready metadata")
			}
		}
		ready <- reply{h, e}
	}()
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case v := <-ready:
		if v.err == nil {
			m := v.header.Metadata
			if !reflect.DeepEqual(m.Inputs, cfg.Inputs) || !reflect.DeepEqual(m.Outputs, cfg.Outputs) || !reflect.DeepEqual(m.InitializedBatches, cfg.BatchSizes) || m.Runtime == "" || m.WarmupIterations < 0 {
				v.err = errors.New("initialized runner metadata differs from execution contract")
			}
			if v.err == nil {
				r.metadata = *m
				return r, nil
			}
		}
		r.Close()
		return nil, v.err
	case <-timer.C:
		r.Close()
		return nil, errors.New("runner startup timed out")
	case <-ctx.Done():
		r.Close()
		return nil, ctx.Err()
	}
}
func (r *Runner) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		// Cancellation terminates the process; these closes only release pipes.
		_ = r.input.Close()
		<-r.exited
		_ = r.output.Close()
	})
}

// runnerOutput is one completed execution: per-request items, output fields
// holding one element per successful request, and batch stage spans.
type runnerOutput struct {
	items                                []itemResult
	outputs                              []Field
	serviceMS, backendMS                 float64
	preprocessMS, postprocessMS, firstMS float64
}

// Only the actor's active batch calls infer. Caller cancellation never reaches
// this context; capacity remains occupied until reply or process termination.
func (r *Runner) infer(ctx context.Context, id uint64, tensors []wireTensor, data [][]byte) (runnerOutput, error) {
	type reply struct {
		o runnerOutput
		e error
	}
	done := make(chan reply, 1)
	go func() {
		started := time.Now()
		o, e := r.exchange(id, tensors, data)
		if e == nil {
			o.serviceMS = milliseconds(time.Since(started))
		}
		done <- reply{o, e}
	}()
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case v := <-done:
		var be *batchError
		if v.e != nil && !errors.As(v.e, &be) {
			r.Close()
		}
		return v.o, v.e
	case <-timer.C:
		r.Close()
		return runnerOutput{}, errors.New("runner execution timed out and process terminated")
	case <-ctx.Done():
		r.Close()
		return runnerOutput{}, ctx.Err()
	}
}
func (r *Runner) exchange(id uint64, tensors []wireTensor, data [][]byte) (runnerOutput, error) {
	if err := writeFrame(r.input, frame{Type: "infer", BatchID: id, Tensors: tensors}, data); err != nil {
		return runnerOutput{}, err
	}
	h, b, e := readFrame(r.output)
	if e != nil {
		return runnerOutput{}, e
	}
	switch h.Type {
	case "error":
		return runnerOutput{}, fmt.Errorf("runner: %s", h.Error)
	case "batch_error":
		if h.BatchID != id || len(b) != 0 || h.Error == "" || len(h.Error) > MaxErrorBytes {
			return runnerOutput{}, errors.New("invalid runner batch error")
		}
		return runnerOutput{}, &batchError{h.Error}
	case "output":
	default:
		return runnerOutput{}, errors.New("invalid runner completion")
	}
	successes, e := r.validateOutput(h, id, tensors[0].Shape[0])
	if e != nil {
		return runnerOutput{}, e
	}
	o := runnerOutput{items: h.Items, outputs: make([]Field, len(h.Tensors)), serviceMS: h.ServiceMS, backendMS: h.BackendMS, preprocessMS: h.PreprocessMS, postprocessMS: h.PostprocessMS, firstMS: h.FirstTokenMS}
	offset := 0
	for i, t := range h.Tensors {
		if t.Name != r.metadata.Outputs[i] || t.Datatype != "BYTES" || len(t.Shape) != 1 || t.Shape[0] != successes || t.Bytes < 0 || t.Bytes > len(b)-offset {
			return runnerOutput{}, errors.New("invalid named output contract")
		}
		elements, e := unframe(b[offset:offset+t.Bytes], successes)
		if e != nil {
			return runnerOutput{}, e
		}
		o.outputs[i] = Field{Name: t.Name, Data: elements}
		offset += t.Bytes
	}
	if offset != len(b) {
		return runnerOutput{}, errors.New("output payload contains unbound bytes")
	}
	return o, nil
}

// validateOutput checks an output frame header and returns its success count.
func (r *Runner) validateOutput(h frame, id uint64, items int) (int, error) {
	stages := h.PreprocessMS + h.BackendMS + h.PostprocessMS
	if h.BatchID != id || len(h.Items) != items || len(h.Tensors) != len(r.metadata.Outputs) || !durationOK(h.ServiceMS) || h.ServiceMS <= 0 || !durationOK(h.PreprocessMS) || !durationOK(h.BackendMS) || !durationOK(h.PostprocessMS) || !durationOK(h.FirstTokenMS) || stages > h.ServiceMS || h.FirstTokenMS > h.BackendMS {
		return 0, errors.New("invalid runner completion")
	}
	successes := 0
	for _, item := range h.Items {
		if (item.Units >= 1) == (item.Error != "") || item.Units < 0 || len(item.Error) > MaxErrorBytes {
			return 0, errors.New("runner item must carry a unit count or a failure reason")
		}
		if item.Units > 0 {
			successes++
		}
	}
	if h.FirstTokenMS != 0 && !(r.generative && successes > 0) {
		return 0, errors.New("first-token span belongs only to generative batches with successes")
	}
	return successes, nil
}
func writeFrame(writer io.Writer, header frame, data [][]byte) error {
	total := 0
	for _, chunk := range data {
		if len(chunk) > MaxFrameBytes-total {
			return errors.New("IPC frame exceeds size limit")
		}
		total += len(chunk)
	}
	header.DataBytes = total
	encoded, e := json.Marshal(header)
	if e != nil {
		return e
	}
	if len(encoded) > MaxHeaderBytes {
		return errors.New("IPC header exceeds size limit")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(encoded)))
	write := func(b []byte) error {
		n, e := writer.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		return e
	}
	if e = write(size[:]); e != nil {
		return e
	}
	if e = write(encoded); e != nil {
		return e
	}
	for _, chunk := range data {
		if e = write(chunk); e != nil {
			return e
		}
	}
	return nil
}
func readFrame(reader io.Reader) (frame, []byte, error) {
	var h frame
	var size [4]byte
	if _, e := io.ReadFull(reader, size[:]); e != nil {
		return h, nil, e
	}
	length := binary.BigEndian.Uint32(size[:])
	if length == 0 || length > MaxHeaderBytes {
		return h, nil, errors.New("invalid IPC header length")
	}
	encoded := make([]byte, length)
	if _, e := io.ReadFull(reader, encoded); e != nil {
		return h, nil, e
	}
	if e := decodeStrict(encoded, &h); e != nil {
		return h, nil, e
	}
	if h.DataBytes < 0 || h.DataBytes > MaxFrameBytes {
		return h, nil, errors.New("invalid IPC payload length")
	}
	data := make([]byte, h.DataBytes)
	_, e := io.ReadFull(reader, data)
	return h, data, e
}
