package firstfloor

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/x448/float16"
)

const (
	datatypeBool = "BOOL"
	datatypeFP16 = "FP16"
	datatypeFP32 = "FP32"
	datatypeFP64 = "FP64"
)

func datatypeBytes(t string) int {
	switch t {
	case datatypeBool, "UINT8", "INT8":
		return 1
	case "UINT16", "INT16", datatypeFP16:
		return 2
	case "UINT32", "INT32", datatypeFP32:
		return 4
	case "UINT64", "INT64", datatypeFP64:
		return 8
	}
	return 0
}
func tensorElements(shape []int) (int, error) {
	n := 1
	for _, d := range shape {
		if d <= 0 || d > MaxFrameBytes/n {
			return 0, errors.New("invalid tensor shape")
		}
		n *= d
	}
	return n, nil
}
func encodeTensor(t Tensor) ([]byte, error) {
	n, e := tensorElements(t.Shape)
	w := datatypeBytes(t.Datatype)
	if e != nil || w == 0 || n > MaxFrameBytes/w {
		return nil, errors.New("invalid tensor shape/datatype")
	}
	var values []json.RawMessage
	if e = json.Unmarshal(t.Data, &values); e != nil || len(values) != n {
		return nil, errors.New("tensor data count differs from shape")
	}
	data := make([]byte, n*w)
	for i, v := range values {
		dst := data[i*w:]
		s := string(v)
		if t.Datatype == datatypeBool {
			if bytes.Equal(v, []byte("true")) {
				dst[0] = 1
			} else if !bytes.Equal(v, []byte("false")) {
				return nil, errors.New("BOOL requires JSON booleans")
			}
			continue
		}
		if bytes.Equal(v, []byte("null")) {
			return nil, errors.New("null tensor element")
		}
		switch t.Datatype {
		case datatypeFP16, datatypeFP32, datatypeFP64:
			bits := 64
			if t.Datatype != datatypeFP64 {
				bits = 32
			}
			f, err := strconv.ParseFloat(s, bits)
			if err != nil || !finite(f) {
				return nil, errors.New("finite numeric tensor element required")
			}
			if t.Datatype == datatypeFP64 {
				binary.LittleEndian.PutUint64(dst, math.Float64bits(f))
			} else if t.Datatype == datatypeFP32 {
				binary.LittleEndian.PutUint32(dst, math.Float32bits(float32(f)))
			} else {
				h := float16.Fromfloat32(float32(f))
				if !finite(float64(h.Float32())) {
					return nil, errors.New("FP16 out of range")
				}
				binary.LittleEndian.PutUint16(dst, h.Bits())
			}
		default:
			var u uint64
			var err error
			if t.Datatype[0] == 'U' {
				u, err = strconv.ParseUint(s, 10, w*8)
			} else {
				var x int64
				x, err = strconv.ParseInt(s, 10, w*8)
				u = uint64(x)
			}
			if err != nil {
				return nil, fmt.Errorf("%s integer: %w", t.Datatype, err)
			}
			switch w {
			case 1:
				dst[0] = byte(u)
			case 2:
				binary.LittleEndian.PutUint16(dst, uint16(u))
			case 4:
				binary.LittleEndian.PutUint32(dst, uint32(u))
			case 8:
				binary.LittleEndian.PutUint64(dst, u)
			}
		}
	}
	return data, nil
}
func decodeTensor(s InputSpec, data []byte) (Tensor, error) {
	n, e := tensorElements(s.Shape)
	w := datatypeBytes(s.Datatype)
	if e != nil || w == 0 || n > MaxFrameBytes/w || len(data) != n*w {
		return Tensor{}, errors.New("invalid output size")
	}
	out := make([]byte, 0, n*4)
	out = append(out, '[')
	for i := range n {
		if i > 0 {
			out = append(out, ',')
		}
		src := data[i*w:]
		var u uint64
		switch w {
		case 1:
			u = uint64(src[0])
		case 2:
			u = uint64(binary.LittleEndian.Uint16(src))
		case 4:
			u = uint64(binary.LittleEndian.Uint32(src))
		case 8:
			u = binary.LittleEndian.Uint64(src)
		}
		switch s.Datatype {
		case datatypeBool:
			if u > 1 {
				return Tensor{}, errors.New("invalid BOOL output")
			}
			out = strconv.AppendBool(out, u == 1)
		case datatypeFP16, datatypeFP32, datatypeFP64:
			var f float64
			switch s.Datatype {
			case datatypeFP16:
				f = float64(float16.Frombits(uint16(u)).Float32())
			case datatypeFP32:
				f = float64(math.Float32frombits(uint32(u)))
			case datatypeFP64:
				f = math.Float64frombits(u)
			}
			if !finite(f) {
				return Tensor{}, errors.New("nonfinite output")
			}
			out = strconv.AppendFloat(out, f, 'g', -1, 64)
		default:
			if s.Datatype[0] == 'U' {
				out = strconv.AppendUint(out, u, 10)
			} else {
				var x int64
				switch w {
				case 1:
					x = int64(int8(u))
				case 2:
					x = int64(int16(u))
				case 4:
					x = int64(int32(u))
				case 8:
					x = int64(u)
				}
				out = strconv.AppendInt(out, x, 10)
			}
		}
	}
	out = append(out, ']')
	return Tensor{Name: s.Name, Datatype: s.Datatype, Shape: s.Shape, Data: out}, nil
}
