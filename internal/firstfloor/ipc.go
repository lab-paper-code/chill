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

type wireTensor struct {
	InputSpec
	Bytes int `json:"bytes"`
}
type frame struct {
	Type       string           `json:"type"`
	BatchID    uint64           `json:"batch_id,omitempty"`
	BatchSizes []int            `json:"batch_sizes,omitempty"`
	Inputs     []InputSpec      `json:"inputs,omitempty"`
	Tensors    []wireTensor     `json:"tensors,omitempty"`
	DataBytes  int              `json:"data_bytes"`
	Error      string           `json:"error,omitempty"`
	Metadata   *RuntimeMetadata `json:"metadata,omitempty"`
	BackendMS  float64          `json:"backend_ms,omitempty"`
	ServiceMS  float64          `json:"service_ms,omitempty"`
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
	r := &Runner{command: command, input: input, output: output, cancel: cancel, exited: make(chan struct{}), timeout: time.Duration(cfg.RunnerTimeoutMS) * time.Millisecond, batchSizes: cfg.BatchSizes}
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
		e := writeFrame(input, frame{Type: "init", Inputs: cfg.Inputs, BatchSizes: cfg.BatchSizes}, nil)
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
			if !reflect.DeepEqual(m.Inputs, cfg.Inputs) || !reflect.DeepEqual(m.InitializedBatches, cfg.BatchSizes) || m.Runtime == "" || m.WarmupIterations < 0 || len(m.Outputs) == 0 {
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

type runnerOutput struct {
	tensors              []wireTensor
	data                 []byte
	serviceMS, backendMS float64
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
		if v.e != nil {
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
	if h.Type == "error" {
		return runnerOutput{}, fmt.Errorf("runner: %s", h.Error)
	}
	if h.Type != "output" || h.BatchID != id || len(h.Tensors) != len(r.metadata.Outputs) || !durationOK(h.ServiceMS) || h.ServiceMS <= 0 || !durationOK(h.BackendMS) || h.BackendMS > h.ServiceMS {
		return runnerOutput{}, errors.New("invalid runner completion")
	}
	offset := 0
	for i, t := range h.Tensors {
		expected := r.metadata.Outputs[i]
		n, e := tensorElements(t.Shape)
		w := datatypeBytes(t.Datatype)
		if t.Name != expected.Name || t.Datatype != expected.Datatype || len(t.Shape) == 0 || !reflect.DeepEqual(t.Shape[1:], expected.Shape) || t.Shape[0] != tensors[0].Shape[0] || e != nil || w == 0 || n > MaxFrameBytes/w || t.Bytes != n*w || t.Bytes > len(b)-offset {
			return runnerOutput{}, errors.New("invalid named output contract")
		}
		offset += t.Bytes
	}
	if offset != len(b) {
		return runnerOutput{}, errors.New("output payload contains unbound bytes")
	}
	return runnerOutput{h.Tensors, b, h.ServiceMS, h.BackendMS}, nil
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
