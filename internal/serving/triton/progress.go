package triton

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lab-paper-code/chill/internal/serving"
)

// ErrNotProgress lets a log scanner ignore unrelated native server output.
var ErrNotProgress = errors.New("not a native CHILL progress record")

const progressMarker = "CHILL_PROGRESS "

// ParseProgress accepts the bounded native record, optionally preceded by the
// Kubernetes timestamps=true prefix. It never infers a response or outcome.
func ParseProgress(line []byte) (serving.BackendProgress, error) {
	var result serving.BackendProgress
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	if !bytes.HasPrefix(line, []byte(progressMarker)) {
		prefix, rest, found := bytes.Cut(line, []byte(" "))
		if !found || len(prefix) > 64 || !bytes.HasPrefix(rest, []byte(progressMarker)) {
			return result, ErrNotProgress
		}
		if _, err := time.Parse(time.RFC3339Nano, string(prefix)); err != nil {
			return result, ErrNotProgress
		}
		line = rest
	}
	if len(line) > 4096 || !utf8.Valid(line) {
		return result, errors.New("native progress record exceeds bound or has invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(line[len(progressMarker):]))
	fields, err := decodeProgress(decoder, &result)
	if err != nil {
		return result, err
	}
	requestID, definition, found := strings.Cut(result.RequestID, "~")
	if !found || definition != result.Definition || !safeIdentity(requestID) || !safeIdentity(result.WorkloadUID) || !definitionIdentity(result.WorkloadUID, result.Definition) {
		return result, errors.New("complete bounded native progress identity required")
	}
	result.RequestID = requestID
	if result.Phase == "queue_trace" {
		if fields != 7 || result.NativeQueue == nil || result.NativeQueue.QueueStartNanos == 0 || (result.NativeQueue.ComputeStartNanos != 0 && result.NativeQueue.ComputeStartNanos < result.NativeQueue.QueueStartNanos) {
			return result, errors.New("complete native queue timing required")
		}
	} else if fields != 5 || result.NativeQueue != nil || (result.Phase != "queued" && result.Phase != "running" && result.Phase != "finished") {
		return result, errors.New("unknown native execution phase")
	}
	return result, nil
}

func decodeProgress(decoder *json.Decoder, result *serving.BackendProgress) (int, error) {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return 0, errors.New("native progress must be a JSON object")
	}
	seen := make(map[string]bool, 7)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return 0, errors.New("invalid native progress JSON")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return 0, errors.New("duplicate native progress field")
		}
		seen[key] = true
		var value string
		if err := decoder.Decode(&value); err != nil {
			return 0, errors.New("native progress fields must be strings")
		}
		switch key {
		case "requestID":
			result.RequestID = value
		case "workloadUID":
			result.WorkloadUID = value
		case "definition":
			result.Definition = value
		case "phase":
			result.Phase = value
		case "at":
			result.At, err = time.Parse(time.RFC3339Nano, value)
			if err != nil || result.At.IsZero() || !strings.HasSuffix(value, "Z") {
				return 0, errors.New("native progress UTC wall time required")
			}
		case "queueStartNanos", "computeStartNanos":
			if result.NativeQueue == nil {
				result.NativeQueue = &serving.NativeQueueTiming{}
			}
			n, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return 0, errors.New("invalid native monotonic queue timestamp")
			}
			if key == "queueStartNanos" {
				result.NativeQueue.QueueStartNanos = n
			} else {
				result.NativeQueue.ComputeStartNanos = n
			}
		default:
			return 0, errors.New("unknown native progress field")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return 0, errors.New("invalid native progress object terminator")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return 0, errors.New("trailing native progress JSON")
	}
	return len(seen), nil
}
