package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	gateway "github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

func (d *profilingLoadDriver) recordInputs(
	ctx context.Context, record profilingRecord,
) (gateway.PreparedNativeInputs, error) {
	d.cache.Lock()
	defer d.cache.Unlock()
	if err := ctx.Err(); err != nil {
		return gateway.PreparedNativeInputs{}, err
	}
	if d.cache.id == record.ID {
		return d.cache.inputs, nil
	}
	// ponytail: one immutable record cached; a byte-bounded LRU is warranted only
	// if measured multi-record disk reads dominate offering lag. Never cache the
	// whole dataset or retain the previous record while preparing its successor.
	d.cache.id, d.cache.inputs = "", gateway.PreparedNativeInputs{}
	inputs := make([]native.Field, len(d.plan.Fields))
	var framed int64
	for index, field := range d.plan.Fields {
		if err := ctx.Err(); err != nil {
			return gateway.PreparedNativeInputs{}, err
		}
		remaining := d.plan.OperatingPoints.FramedDecodedRequestUpperBytes - framed - 4
		if remaining < 0 {
			return gateway.PreparedNativeInputs{}, errors.New("dataset record exceeds frozen framed decoded request upper bytes")
		}
		file, err := os.Open(filepath.Join(record.directory, field))
		if err != nil {
			return gateway.PreparedNativeInputs{}, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, remaining+1))
		if err := errors.Join(readErr, file.Close()); err != nil {
			return gateway.PreparedNativeInputs{}, err
		}
		if int64(len(data)) > remaining {
			return gateway.PreparedNativeInputs{}, errors.New("dataset record exceeds frozen framed decoded request upper bytes")
		}
		framed += 4 + int64(len(data))
		inputs[index] = native.Field{Name: field, Data: [][]byte{data}}
	}
	prepared, err := gateway.PrepareNativeInputs(inputs)
	if err != nil {
		return gateway.PreparedNativeInputs{}, err
	}
	d.cache.id, d.cache.inputs = record.ID, prepared
	return prepared, nil
}
